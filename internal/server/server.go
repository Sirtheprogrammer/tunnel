package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"sync"
	"time"

	"github.com/hashicorp/yamux"

	"tunnel/internal/names"
	"tunnel/internal/proto"
)

// Config holds the runtime settings for a Server.
type Config struct {
	// Domain is the base domain tunnels are published under, e.g.
	// "tl.codesky.tech". Public URLs are https://<label>.<Domain>.
	Domain string

	// ControlAddr is the listen address for agent connections.
	ControlAddr string

	// MaxTunnelsPerAccount caps concurrent tunnels; zero means unlimited.
	MaxTunnelsPerAccount int

	// DisconnectLease is how long a subdomain is held for its owner after the
	// agent disconnects, so a reconnect keeps the same URL. Zero disables it.
	DisconnectLease time.Duration

	// AuthTimeout bounds how long an agent may take to authenticate before the
	// connection is dropped.
	AuthTimeout time.Duration

	// PublicScheme and PublicPort shape generated URLs. They exist so local
	// development can advertise http://<label>.lvh.me:8443 instead of https on
	// the standard port.
	PublicScheme string
	PublicPort   int

	// Auth validates agent tokens. Required.
	Auth Authenticator

	// TLSConfig secures the control listener. Nil serves plaintext, which is
	// only appropriate for tests and local development.
	TLSConfig *tls.Config

	Logger *slog.Logger
}

// Authenticator validates agent tokens and decides who may claim a specific
// subdomain. The SQLite-backed implementation lives in internal/store; the
// server only depends on this interface so it can be tested without a database.
type Authenticator interface {
	// Authenticate resolves a token to an account ID. It returns an error if
	// the token is unknown, revoked, or belongs to a disabled account.
	Authenticate(ctx context.Context, token string) (accountID string, err error)

	// AllowSubdomain reports whether accountID may claim label. It is only
	// consulted for user-chosen labels, never for generated ones.
	AllowSubdomain(ctx context.Context, accountID, label string) error
}

func (c *Config) setDefaults() {
	if c.ControlAddr == "" {
		c.ControlAddr = fmt.Sprintf(":%d", proto.ControlPort)
	}
	if c.AuthTimeout == 0 {
		c.AuthTimeout = 15 * time.Second
	}
	if c.PublicScheme == "" {
		c.PublicScheme = "https"
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
}

// Server accepts agent connections on the control listener and serves public
// traffic on the data plane.
type Server struct {
	cfg      Config
	log      *slog.Logger
	registry *Registry
	proxy    *httputil.ReverseProxy

	mu       sync.Mutex
	sessions map[string]*Session

	done     chan struct{}
	stopOnce sync.Once
}

// New returns a Server. It does not listen; call ServeControl and use Handler.
func New(cfg Config) (*Server, error) {
	cfg.setDefaults()
	if cfg.Domain == "" {
		return nil, errors.New("server: Domain is required")
	}
	if cfg.Auth == nil {
		return nil, errors.New("server: Auth is required")
	}
	s := &Server{
		cfg:      cfg,
		log:      cfg.Logger,
		registry: NewRegistry(cfg.DisconnectLease),
		sessions: make(map[string]*Session),
		done:     make(chan struct{}),
	}
	s.proxy = s.newReverseProxy()
	if cfg.DisconnectLease > 0 {
		go s.registry.SweepLoop(s.done, cfg.DisconnectLease)
	}
	return s, nil
}

// Registry exposes the tunnel registry, for status endpoints and tests.
func (s *Server) Registry() *Registry { return s.registry }

// publicURL builds the advertised URL for a label.
func (s *Server) publicURL(label string) string {
	host := label + "." + s.cfg.Domain
	if p := s.cfg.PublicPort; p != 0 && !isDefaultPort(s.cfg.PublicScheme, p) {
		host = fmt.Sprintf("%s:%d", host, p)
	}
	return s.cfg.PublicScheme + "://" + host
}

func isDefaultPort(scheme string, port int) bool {
	return (scheme == "https" && port == 443) || (scheme == "http" && port == 80)
}

// validateSubdomain checks a user-chosen label for syntax, reservation, and
// account entitlement.
func (s *Server) validateSubdomain(label, accountID string) (string, error) {
	label, err := names.Validate(label)
	if err != nil {
		return "", err
	}
	if err := s.cfg.Auth.AllowSubdomain(context.Background(), accountID, label); err != nil {
		return "", err
	}
	return label, nil
}

// Close stops background work and drops every agent session.
func (s *Server) Close() error {
	s.stopOnce.Do(func() { close(s.done) })
	s.mu.Lock()
	sessions := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.Unlock()
	for _, sess := range sessions {
		sess.close(errors.New("server shutting down"))
	}
	return nil
}

// ServeControl accepts agent connections on ln until the listener is closed.
func (s *Server) ServeControl(ctx context.Context, ln net.Listener) error {
	if s.cfg.TLSConfig != nil {
		ln = tls.NewListener(ln, s.cfg.TLSConfig)
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-s.done:
		}
		ln.Close()
	}()

	s.log.Info("control listener started", "addr", ln.Addr().String())
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			case <-s.done:
				return nil
			default:
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue // transient; keep accepting
			}
			return fmt.Errorf("control listener: %w", err)
		}
		go s.handleAgent(ctx, conn)
	}
}

// handleAgent authenticates one agent connection and runs its control loop.
func (s *Server) handleAgent(ctx context.Context, conn net.Conn) {
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(30 * time.Second)
	}

	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 20 * time.Second
	// A dead peer should be noticed well before a TCP timeout would fire, so a
	// disconnected agent releases its subdomain promptly.
	cfg.ConnectionWriteTimeout = 20 * time.Second
	cfg.LogOutput = nil
	cfg.Logger = newYamuxLogger(s.log)

	mux, err := yamux.Server(conn, cfg)
	if err != nil {
		s.log.Warn("yamux handshake failed", "remote", conn.RemoteAddr(), "error", err)
		conn.Close()
		return
	}

	// The agent opens the control stream first.
	if err := conn.SetReadDeadline(time.Now().Add(s.cfg.AuthTimeout)); err != nil {
		s.log.Warn("set auth deadline", "error", err)
	}
	ctlStream, err := mux.Accept()
	if err != nil {
		mux.Close()
		conn.Close()
		return
	}

	sess := &Session{
		id:      newID(),
		conn:    conn,
		mux:     mux,
		ctl:     proto.NewConn(ctlStream),
		srv:     s,
		tunnels: make(map[string]*Tunnel),
		closed:  make(chan struct{}),
	}
	sess.log = s.log.With("session", sess.id, "remote", conn.RemoteAddr().String())

	if err := s.authenticate(ctx, sess); err != nil {
		sess.log.Info("authentication rejected", "error", err)
		mux.Close()
		conn.Close()
		return
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil { // clear the deadline
		sess.log.Warn("clear auth deadline", "error", err)
	}

	s.mu.Lock()
	s.sessions[sess.id] = sess
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.sessions, sess.id)
		s.mu.Unlock()
	}()

	sess.log.Info("agent connected", "account", sess.accountID)
	sess.serve(ctx)
}

// authenticate performs the auth handshake. On failure it tells the agent why
// before the caller closes the connection, so the CLI can print a real message.
func (s *Server) authenticate(ctx context.Context, sess *Session) error {
	env, err := sess.ctl.ReadEnvelope()
	if err != nil {
		return fmt.Errorf("read auth: %w", err)
	}
	if env.Type != proto.TypeAuth {
		return fmt.Errorf("expected %s, got %s", proto.TypeAuth, env.Type)
	}
	var auth proto.Auth
	if err := env.Decode(&auth); err != nil {
		return err
	}

	reject := func(msg string) error {
		sess.writeEnvelope(proto.TypeAuthResp, env.ID, &proto.AuthResp{
			OK:      false,
			Version: proto.Version,
			Error:   msg,
		})
		return errors.New(msg)
	}

	if auth.Protocol != proto.Version {
		return reject(fmt.Sprintf(
			"protocol version mismatch: server speaks %s, this client speaks %s; please update tunnelx",
			proto.Version, auth.Protocol))
	}

	accountID, err := s.cfg.Auth.Authenticate(ctx, auth.Token)
	if err != nil {
		return reject(err.Error())
	}
	sess.accountID = accountID

	return sess.writeEnvelope(proto.TypeAuthResp, env.ID, &proto.AuthResp{
		OK:        true,
		AccountID: accountID,
		Version:   proto.Version,
	})
}

// Handler returns the data-plane HTTP handler.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serveHTTP)
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	label, ok := names.LabelFor(r.Host, s.cfg.Domain)
	if !ok {
		s.writeErrorPage(w, r, http.StatusNotFound, pageNoSuchHost)
		return
	}
	t, ok := s.registry.Lookup(label)
	if !ok {
		s.writeErrorPage(w, r, http.StatusNotFound, pageTunnelNotFound)
		return
	}

	reqID := newID()
	ctx := context.WithValue(r.Context(), ctxTunnel, t)
	ctx = context.WithValue(ctx, ctxRequestID, reqID)
	r = r.WithContext(ctx)

	if isUpgrade(r) {
		s.serveUpgrade(w, r, t, reqID)
		return
	}
	s.proxy.ServeHTTP(w, r)
}

// RedirectHandler serves the plaintext listener, sending everything to HTTPS.
func (s *Server) RedirectHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		u := *r.URL
		u.Scheme = "https"
		u.Host = host
		http.Redirect(w, r, u.String(), http.StatusMovedPermanently)
	})
}
