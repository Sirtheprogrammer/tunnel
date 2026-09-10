package agent

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"

	"tunnel/internal/proto"
)

// session is one live connection to the server.
type session struct {
	agent *Agent
	mux   *yamux.Session
	ctl   *proto.Conn
	log   *slog.Logger

	writeMu sync.Mutex

	// pend correlates replies to in-flight control requests by envelope ID.
	pendMu sync.Mutex
	pend   map[string]chan *proto.Envelope

	tunnelID string
}

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func (s *session) write(t proto.Type, id string, payload any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.ctl.WriteEnvelope(t, id, payload)
}

// authenticate performs the handshake. A rejection is fatal: retrying with the
// same bad token would just spin.
func (s *session) authenticate(ctx context.Context, token string) error {
	id := newID()
	if err := s.write(proto.TypeAuth, id, authPayload(token)); err != nil {
		return fmt.Errorf("send auth: %w", err)
	}
	env, err := s.ctl.ReadEnvelope()
	if err != nil {
		return fmt.Errorf("read auth response: %w", err)
	}
	if env.Type != proto.TypeAuthResp {
		return fmt.Errorf("expected auth response, got %s", env.Type)
	}
	var resp proto.AuthResp
	if err := env.Decode(&resp); err != nil {
		return err
	}
	if !resp.OK {
		return &fatalError{err: errors.New(resp.Error)}
	}
	return nil
}

// serve requests the tunnel, then dispatches inbound streams until the session
// ends. It reports whether the tunnel became live before failing.
func (s *session) serve(ctx context.Context) (connected bool, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// The control read loop must be running before we send tunnel.create, since
	// that is how the reply is delivered.
	ctlErr := make(chan error, 1)
	go func() { ctlErr <- s.readControl() }()

	created, err := s.createTunnel(ctx)
	if err != nil {
		return false, err
	}
	s.tunnelID = created.ID

	s.agent.mu.Lock()
	s.agent.url = created.URL
	s.agent.mu.Unlock()
	s.agent.setState(StateConnected, nil)
	if o := s.agent.cfg.Observer; o != nil {
		o.TunnelOpened(created.URL)
	}
	s.log.Info("tunnel ready", "url", created.URL, "forwarding", s.agent.cfg.LocalAddr)

	go s.pingLoop(ctx)

	// Accept data streams the server opens for inbound requests.
	accept := make(chan error, 1)
	go func() {
		for {
			stream, err := s.mux.Accept()
			if err != nil {
				accept <- err
				return
			}
			go s.handleStream(stream)
		}
	}()

	select {
	case <-ctx.Done():
		return true, ctx.Err()
	case err := <-ctlErr:
		return true, fmt.Errorf("control stream: %w", err)
	case err := <-accept:
		return true, fmt.Errorf("session ended: %w", err)
	}
}

// createTunnel sends tunnel.create and waits for the reply.
func (s *session) createTunnel(ctx context.Context) (*proto.TunnelCreated, error) {
	req := &proto.TunnelCreate{
		Proto:     proto.ProtoHTTP,
		Subdomain: s.agent.cfg.Subdomain,
		LocalAddr: s.agent.cfg.LocalAddr,
	}
	env, err := s.request(ctx, proto.TypeTunnelCreate, req, 30*time.Second)
	if err != nil {
		return nil, err
	}
	switch env.Type {
	case proto.TypeTunnelCreated:
		var created proto.TunnelCreated
		if err := env.Decode(&created); err != nil {
			return nil, err
		}
		return &created, nil
	case proto.TypeError:
		var e proto.ErrorMsg
		if err := env.Decode(&e); err != nil {
			return nil, err
		}
		// A bad or taken subdomain will not fix itself; surface it and stop
		// rather than reconnecting in a loop.
		if e.Code == proto.CodeSubdomainInvalid || e.Code == proto.CodeSubdomainTaken ||
			e.Code == proto.CodeUnsupported || e.Code == proto.CodeTunnelLimit {
			return nil, &fatalError{err: &e}
		}
		return nil, &e
	default:
		return nil, fmt.Errorf("unexpected reply %s to tunnel.create", env.Type)
	}
}

// request sends a control message and waits for the reply carrying the same ID.
func (s *session) request(ctx context.Context, t proto.Type, payload any, timeout time.Duration) (*proto.Envelope, error) {
	id := newID()
	ch := make(chan *proto.Envelope, 1)

	s.pendMu.Lock()
	s.pend[id] = ch
	s.pendMu.Unlock()
	defer func() {
		s.pendMu.Lock()
		delete(s.pend, id)
		s.pendMu.Unlock()
	}()

	if err := s.write(t, id, payload); err != nil {
		return nil, err
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, fmt.Errorf("timed out waiting for reply to %s", t)
	case env := <-ch:
		return env, nil
	}
}

// readControl runs the control-stream read loop, routing replies to waiters.
func (s *session) readControl() error {
	for {
		env, err := s.ctl.ReadEnvelope()
		if err != nil {
			return err
		}
		if env.ID != "" {
			s.pendMu.Lock()
			ch, ok := s.pend[env.ID]
			s.pendMu.Unlock()
			if ok {
				select {
				case ch <- env:
				default:
					s.log.Warn("dropping duplicate control response", "id", env.ID)
				}
				continue
			}
		}
		if env.Type == proto.TypePing {
			if err := s.write(proto.TypePong, env.ID, nil); err != nil {
				return err
			}
		}
	}
}

// pingLoop measures round-trip latency so the dashboard can show it.
func (s *session) pingLoop(ctx context.Context) {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			start := time.Now()
			if _, err := s.request(ctx, proto.TypePing, nil, 10*time.Second); err != nil {
				return // the session is failing; the accept loop will report it
			}
			s.agent.mu.Lock()
			s.agent.rtt = time.Since(start)
			s.agent.mu.Unlock()
		}
	}
}

// handleStream serves one inbound request stream.
func (s *session) handleStream(stream net.Conn) {
	defer stream.Close()

	br := bufio.NewReader(stream)
	hdr, err := proto.ReadStreamHeader(br)
	if err != nil {
		if !errors.Is(err, io.EOF) {
			s.log.Debug("read stream header", "error", err)
		}
		return
	}
	if err := s.forward(stream, br, hdr); err != nil && !isBenignClose(err) {
		s.log.Debug("forward failed", "request", hdr.RequestID, "error", err)
	}
}

func isBenignClose(err error) bool {
	return errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, yamux.ErrStreamClosed) ||
		errors.Is(err, yamux.ErrSessionShutdown) ||
		errors.Is(err, net.ErrClosed)
}

// Close gracefully closes the session by sending a tunnel.close message.
func (s *session) Close() {
	if s.tunnelID != "" {
		s.write(proto.TypeTunnelClose, newID(), nil)
	}
}
