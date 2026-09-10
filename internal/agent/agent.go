// Package agent implements the client half of a tunnel: it dials the server,
// authenticates, requests a public URL, and forwards inbound streams to a local
// address.
package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/hashicorp/yamux"

	"tunnel/internal/proto"
)

// HostHeaderMode controls what Host header the local service sees.
type HostHeaderMode string

const (
	// HostPreserve forwards the public hostname unchanged, so vhost-based apps
	// and absolute-URL generation behave as they would in production.
	HostPreserve HostHeaderMode = "preserve"
	// HostRewrite replaces Host with the local target. Many dev servers (Vite,
	// webpack-dev-server, Rails) reject unknown Host values, so this is the fix
	// for the most common "it works locally but not through the tunnel" report.
	HostRewrite HostHeaderMode = "rewrite"
)

// Config configures an Agent.
type Config struct {
	// ServerAddr is the control endpoint, host:port.
	ServerAddr string

	// Token authenticates the agent.
	Token string

	// LocalAddr is the address requests are forwarded to, host:port.
	LocalAddr string

	// Subdomain requests a specific label; empty asks the server to pick.
	Subdomain string

	// HostHeader selects the Host rewriting policy. Empty means HostPreserve.
	HostHeader HostHeaderMode

	// HostHeaderValue overrides Host with a literal value when set, taking
	// precedence over HostHeader.
	HostHeaderValue string

	// Insecure skips server certificate verification. Development only.
	Insecure bool

	CACert string

	// TLSDisabled connects in plaintext. Local development and tests only.
	TLSDisabled bool

	// Observer receives lifecycle and traffic events for the dashboards. It may
	// be nil.
	Observer Observer

	Logger *slog.Logger
}

// Observer receives events an Agent produces. Implementations must not block;
// the inspector satisfies this by buffering.
type Observer interface {
	// StateChanged reports a connection state transition.
	StateChanged(State)
	// TunnelOpened reports the public URL once the server assigns it.
	TunnelOpened(url string)
	// Exchange reports one completed request/response.
	Exchange(*Exchange)
}

// State is the agent connection state, surfaced in the terminal UI.
type State string

const (
	StateConnecting   State = "connecting"
	StateConnected    State = "connected"
	StateReconnecting State = "reconnecting"
	StateStopped      State = "stopped"
)

// Agent maintains one tunnel, reconnecting for as long as Run is executing.
type Agent struct {
	cfg Config
	log *slog.Logger

	mu     sync.RWMutex
	url    string
	state  State
	rtt    time.Duration
	lastEr error
}

// New validates cfg and returns an Agent.
func New(cfg Config) (*Agent, error) {
	if cfg.ServerAddr == "" {
		return nil, errors.New("agent: ServerAddr is required")
	}
	if cfg.LocalAddr == "" {
		return nil, errors.New("agent: LocalAddr is required")
	}
	if cfg.HostHeader == "" {
		cfg.HostHeader = HostPreserve
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	return &Agent{cfg: cfg, log: cfg.Logger, state: StateConnecting}, nil
}

// URL returns the current public URL, empty before the first successful
// connection.
func (a *Agent) URL() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.url
}

// State returns the current connection state and the most recent error.
func (a *Agent) State() (State, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.state, a.lastEr
}

// RTT returns the last measured round trip to the server.
func (a *Agent) RTT() time.Duration {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.rtt
}

func (a *Agent) setState(s State, err error) {
	a.mu.Lock()
	a.state, a.lastEr = s, err
	a.mu.Unlock()
	if a.cfg.Observer != nil {
		a.cfg.Observer.StateChanged(s)
	}
}

// Run connects and serves until ctx is cancelled, reconnecting with backoff.
//
// It returns early only for errors that retrying cannot fix: a rejected token
// or a protocol version mismatch. Everything else — the server restarting, a
// laptop waking from sleep, flaky wifi — is retried indefinitely, because a
// tunnel that silently dies mid-demo is worse than one that takes a few seconds
// to come back.
func (a *Agent) Run(ctx context.Context) error {
	backoff := newBackoff()
	first := true
	for {
		connected, err := a.runOnce(ctx, first)
		if ctx.Err() != nil {
			a.setState(StateStopped, nil)
			return nil
		}
		var fatal *fatalError
		if errors.As(err, &fatal) {
			a.setState(StateStopped, fatal.err)
			return fatal.err
		}
		first = false
		if connected {
			// The tunnel worked, so this is a fresh outage rather than a
			// continuing one; come back quickly instead of at the old ceiling.
			backoff.reset()
		}

		wait := backoff.next()
		a.log.Warn("connection lost, retrying", "error", err, "in", wait.Round(time.Millisecond))
		a.setState(StateReconnecting, err)
		select {
		case <-ctx.Done():
			a.setState(StateStopped, nil)
			return nil
		case <-time.After(wait):
		}
	}
}

// fatalError marks a failure that reconnecting cannot resolve.
type fatalError struct{ err error }

func (e *fatalError) Error() string { return e.err.Error() }
func (e *fatalError) Unwrap() error { return e.err }

// runOnce establishes one session and serves it until it drops. It reports
// whether the tunnel became live, which decides if the backoff resets.
func (a *Agent) runOnce(ctx context.Context, first bool) (connected bool, err error) {
	if first {
		a.setState(StateConnecting, nil)
	}

	conn, err := a.dial(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()

	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 20 * time.Second
	cfg.ConnectionWriteTimeout = 20 * time.Second
	cfg.LogOutput = nil
	cfg.Logger = newYamuxLogger(a.log)

	mux, err := yamux.Client(conn, cfg)
	if err != nil {
		return false, fmt.Errorf("start session: %w", err)
	}
	defer mux.Close()

	ctlStream, err := mux.Open()
	if err != nil {
		return false, fmt.Errorf("open control stream: %w", err)
	}
	sess := &session{
		agent: a,
		mux:   mux,
		ctl:   proto.NewConn(ctlStream),
		log:   a.log,
		pend:  make(map[string]chan *proto.Envelope),
	}
	defer sess.Close()

	if err := sess.authenticate(ctx, a.cfg.Token); err != nil {
		return false, err
	}
	return sess.serve(ctx)
}

// dial opens the transport connection to the server.
func (a *Agent) dial(ctx context.Context) (net.Conn, error) {
	d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	if a.cfg.TLSDisabled {
		conn, err := d.DialContext(ctx, "tcp", a.cfg.ServerAddr)
		if err != nil {
			return nil, fmt.Errorf("dial %s: %w", a.cfg.ServerAddr, err)
		}
		return conn, nil
	}

	host, _, err := net.SplitHostPort(a.cfg.ServerAddr)
	if err != nil {
		host = a.cfg.ServerAddr
	}
	td := &tls.Dialer{
		NetDialer: d,
		Config: &tls.Config{
			ServerName:         host,
			InsecureSkipVerify: a.cfg.Insecure,
			MinVersion:         tls.VersionTLS12,
		},
	}
	
	if a.cfg.CACert != "" {
		b, err := os.ReadFile(a.cfg.CACert)
		if err == nil {
			pool, _ := x509.SystemCertPool()
			if pool == nil {
				pool = x509.NewCertPool()
			}
			pool.AppendCertsFromPEM(b)
			td.Config.RootCAs = pool
		}
	}
	conn, err := td.DialContext(ctx, "tcp", a.cfg.ServerAddr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", a.cfg.ServerAddr, err)
	}
	return conn, nil
}

// agentVersion is stamped at build time; it is reported to the server and shown
// by `tunnelx version`.
var agentVersion = "dev"

// Version returns the agent build version.
func Version() string { return agentVersion }

// SetVersion overrides the reported version, called from main at startup.
func SetVersion(v string) {
	if v != "" {
		agentVersion = v
	}
}

func authPayload(token string) *proto.Auth {
	host, _ := os.Hostname()
	return &proto.Auth{
		Protocol: proto.Version,
		Token:    token,
		Version:  agentVersion,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Hostname: host,
	}
}
