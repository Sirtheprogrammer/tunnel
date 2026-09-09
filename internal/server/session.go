package server

import (
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

// Session is one connected agent: a yamux session over a TLS connection, plus
// the control stream and the tunnels the agent has opened.
type Session struct {
	id        string
	accountID string
	conn      net.Conn
	mux       *yamux.Session
	ctl       *proto.Conn
	log       *slog.Logger
	srv       *Server

	// writeMu serialises control-stream writes; the read loop is single-
	// threaded but writes come from both the read loop and the ping timer.
	writeMu sync.Mutex

	mu      sync.Mutex
	tunnels map[string]*Tunnel

	closeOnce sync.Once
	closed    chan struct{}
}

// newID returns a short random identifier for sessions, tunnels and requests.
func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail in practice on any supported platform, and
		// these IDs are for correlation rather than security.
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// OpenStream opens a data stream to the agent. The caller owns the returned
// stream and must close it.
func (s *Session) OpenStream() (net.Conn, error) {
	if s.mux.IsClosed() {
		return nil, errors.New("agent session is closed")
	}
	return s.mux.Open()
}

// Closed returns a channel closed when the session ends.
func (s *Session) Closed() <-chan struct{} { return s.closed }

// close tears down the session and unregisters every tunnel it served.
func (s *Session) close(cause error) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		tunnels := make([]*Tunnel, 0, len(s.tunnels))
		for _, t := range s.tunnels {
			tunnels = append(tunnels, t)
		}
		s.tunnels = nil
		s.mu.Unlock()

		for _, t := range tunnels {
			s.srv.registry.Unregister(t)
		}
		s.mux.Close()
		s.conn.Close()
		close(s.closed)

		if cause != nil && !isBenignClose(cause) {
			s.log.Info("agent disconnected", "tunnels", len(tunnels), "cause", cause)
		} else {
			s.log.Info("agent disconnected", "tunnels", len(tunnels))
		}
	})
}

// isBenignClose reports whether err is an ordinary disconnect rather than a
// fault worth surfacing. Agents come and go constantly, so logging every EOF at
// warning level would bury real problems.
func isBenignClose(err error) bool {
	return errors.Is(err, io.EOF) ||
		errors.Is(err, yamux.ErrSessionShutdown) ||
		errors.Is(err, yamux.ErrStreamClosed) ||
		errors.Is(err, net.ErrClosed)
}

// writeEnvelope sends a control message, serialising concurrent writers.
func (s *Session) writeEnvelope(t proto.Type, id string, payload any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.ctl.WriteEnvelope(t, id, payload)
}

// writeError replies to request id with a protocol error.
func (s *Session) writeError(id, code, msg string) error {
	return s.writeEnvelope(proto.TypeError, id, &proto.ErrorMsg{Code: code, Message: msg})
}

// serve runs the control loop until the agent disconnects or misbehaves.
func (s *Session) serve(ctx context.Context) {
	defer s.close(nil)

	// Stop the read loop when the server shuts down.
	go func() {
		select {
		case <-ctx.Done():
			s.close(ctx.Err())
		case <-s.closed:
		}
	}()

	for {
		env, err := s.ctl.ReadEnvelope()
		if err != nil {
			s.close(err)
			return
		}
		if err := s.handle(env); err != nil {
			s.log.Warn("control message failed", "type", env.Type, "error", err)
			s.close(err)
			return
		}
	}
}

// handle dispatches one control message. A returned error is fatal to the
// session; per-request failures are reported over the wire instead.
func (s *Session) handle(env *proto.Envelope) error {
	switch env.Type {
	case proto.TypeTunnelCreate:
		return s.handleTunnelCreate(env)
	case proto.TypeTunnelClose:
		return s.handleTunnelClose(env)
	case proto.TypePing:
		return s.writeEnvelope(proto.TypePong, env.ID, nil)
	case proto.TypePong:
		return nil
	default:
		// An unknown message is a version skew, not a reason to drop a working
		// session; tell the agent and carry on.
		return s.writeError(env.ID, proto.CodeUnsupported,
			fmt.Sprintf("unsupported message type %q", env.Type))
	}
}

func (s *Session) handleTunnelCreate(env *proto.Envelope) error {
	var req proto.TunnelCreate
	if err := env.Decode(&req); err != nil {
		return err
	}
	if req.Proto == "" {
		req.Proto = proto.ProtoHTTP
	}
	if req.Proto != proto.ProtoHTTP {
		return s.writeError(env.ID, proto.CodeUnsupported,
			fmt.Sprintf("protocol %q is not supported yet; only http is available", req.Proto))
	}

	if max := s.srv.cfg.MaxTunnelsPerAccount; max > 0 && s.srv.registry.CountFor(s.accountID) >= max {
		return s.writeError(env.ID, proto.CodeTunnelLimit,
			fmt.Sprintf("account already has the maximum of %d open tunnels", max))
	}

	t := &Tunnel{
		ID:        newID(),
		AccountID: s.accountID,
		LocalAddr: req.LocalAddr,
		Proto:     req.Proto,
		CreatedAt: time.Now(),
		sess:      s,
	}

	if req.Subdomain == "" {
		if err := s.srv.registry.RegisterGenerated(t); err != nil {
			s.log.Error("could not allocate subdomain", "error", err)
			return s.writeError(env.ID, proto.CodeInternal, "could not allocate a subdomain")
		}
	} else {
		label, err := s.srv.validateSubdomain(req.Subdomain, s.accountID)
		if err != nil {
			return s.writeError(env.ID, proto.CodeSubdomainInvalid, err.Error())
		}
		t.Label = label
		if err := s.srv.registry.Register(t); err != nil {
			return s.writeError(env.ID, proto.CodeSubdomainTaken, err.Error())
		}
	}

	t.URL = s.srv.publicURL(t.Label)

	s.mu.Lock()
	if s.tunnels == nil { // session closed while we were registering
		s.mu.Unlock()
		s.srv.registry.Unregister(t)
		return errors.New("session closed")
	}
	s.tunnels[t.ID] = t
	s.mu.Unlock()

	s.log.Info("tunnel opened", "label", t.Label, "url", t.URL, "local", t.LocalAddr)
	return s.writeEnvelope(proto.TypeTunnelCreated, env.ID, &proto.TunnelCreated{
		ID:        t.ID,
		Subdomain: t.Label,
		URL:       t.URL,
	})
}

func (s *Session) handleTunnelClose(env *proto.Envelope) error {
	var req proto.TunnelClose
	if err := env.Decode(&req); err != nil {
		return err
	}
	s.mu.Lock()
	t, ok := s.tunnels[req.ID]
	if ok {
		delete(s.tunnels, req.ID)
	}
	s.mu.Unlock()
	if !ok {
		return nil // already gone; closing twice is not an error
	}
	s.srv.registry.Unregister(t)
	s.log.Info("tunnel closed", "label", t.Label)
	return nil
}
