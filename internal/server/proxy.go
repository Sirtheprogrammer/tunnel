package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"

	"tunnel/internal/proto"
)

type ctxKey int

const (
	ctxTunnel ctxKey = iota
	ctxRequestID
)

// tunnelFrom returns the tunnel the data-plane handler resolved for this
// request.
func tunnelFrom(ctx context.Context) (*Tunnel, bool) {
	t, ok := ctx.Value(ctxTunnel).(*Tunnel)
	return t, ok
}

// newReverseProxy builds the single ReverseProxy used for every tunnel. The
// tunnel itself travels in the request context, so one instance serves all of
// them and we avoid allocating a proxy per connection.
func (s *Server) newReverseProxy() *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Transport: streamTransport{},
		Rewrite: func(pr *httputil.ProxyRequest) {
			// The agent reconstructs the request against the local target, so
			// the URL host here only has to be well-formed. Host is preserved
			// so vhost-based apps see the public name; rewriting to the local
			// address is the agent's job, driven by --host-header.
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = pr.In.Host
			pr.Out.Host = pr.In.Host

			pr.SetXForwarded()
			pr.Out.Header.Set("X-Forwarded-Proto", "https")
			pr.Out.Header.Set("X-Forwarded-Host", pr.In.Host)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			s.log.Warn("proxy error", "host", r.Host, "path", r.URL.Path, "error", err)
			s.writeErrorPage(w, r, http.StatusBadGateway, pageAgentUnreachable)
		},
	}
}

// streamTransport carries one HTTP exchange over a fresh yamux stream to the
// agent that owns the tunnel.
type streamTransport struct{}

func (streamTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t, ok := tunnelFrom(req.Context())
	if !ok {
		return nil, errors.New("no tunnel bound to request")
	}
	reqID, _ := req.Context().Value(ctxRequestID).(string)

	stream, err := t.sess.OpenStream()
	if err != nil {
		return nil, fmt.Errorf("open stream to agent: %w", err)
	}

	hdr := &proto.StreamHeader{
		TunnelID:   t.ID,
		RequestID:  reqID,
		RemoteAddr: req.Header.Get("X-Forwarded-For"),
	}
	if err := proto.WriteStreamHeader(stream, hdr); err != nil {
		stream.Close()
		return nil, err
	}

	// Write the request from a separate goroutine while reading the response
	// here. Doing both on one goroutine deadlocks whenever a local server
	// answers before consuming the whole request body (a 413, say): our write
	// would block on a full flow-control window that only our own read can
	// drain.
	writeDone := make(chan error, 1)
	go func() { writeDone <- req.Write(stream) }()

	resp, err := http.ReadResponse(bufio.NewReader(stream), req)
	if err != nil {
		stream.Close()
		// A write failure is the more informative error when both fail.
		if werr := <-writeDone; werr != nil {
			return nil, fmt.Errorf("write request to agent: %w", werr)
		}
		return nil, fmt.Errorf("read response from agent: %w", err)
	}
	resp.Body = &streamBody{ReadCloser: resp.Body, stream: stream}
	return resp, nil
}

// streamBody ties the response body's lifetime to its stream, so finishing or
// abandoning a response always releases the stream.
type streamBody struct {
	io.ReadCloser
	stream net.Conn
}

func (b *streamBody) Close() error {
	err := b.ReadCloser.Close()
	// Closing the stream also unblocks a request-write goroutine that is still
	// running because the local server never read the full body.
	if cerr := b.stream.Close(); err == nil && !isBenignClose(cerr) {
		err = cerr
	}
	return err
}

// isUpgrade reports whether r asks to switch protocols, which ReverseProxy
// cannot carry for us because the connection stops being HTTP after the 101.
func isUpgrade(r *http.Request) bool {
	if !headerHasToken(r.Header["Connection"], "Upgrade") {
		return false
	}
	return r.Header.Get("Upgrade") != ""
}

// headerHasToken reports whether a comma-separated header list contains token,
// case-insensitively. Connection is a list header, so a plain equality check
// would miss "keep-alive, Upgrade".
func headerHasToken(values []string, token string) bool {
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

// serveUpgrade proxies a protocol upgrade (WebSocket and friends). It hijacks
// the client connection and splices it to a stream once the agent has relayed
// a 101 response.
func (s *Server) serveUpgrade(w http.ResponseWriter, r *http.Request, t *Tunnel, reqID string) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		s.writeErrorPage(w, r, http.StatusInternalServerError, pageInternal)
		return
	}

	stream, err := t.sess.OpenStream()
	if err != nil {
		s.log.Warn("upgrade: open stream", "host", r.Host, "error", err)
		s.writeErrorPage(w, r, http.StatusBadGateway, pageAgentUnreachable)
		return
	}
	defer stream.Close()

	hdr := &proto.StreamHeader{
		TunnelID:   t.ID,
		RequestID:  reqID,
		RemoteAddr: clientIP(r),
		Upgrade:    true,
	}
	if err := proto.WriteStreamHeader(stream, hdr); err != nil {
		s.writeErrorPage(w, r, http.StatusBadGateway, pageAgentUnreachable)
		return
	}

	outreq := r.Clone(r.Context())
	outreq.URL.Scheme = "http"
	outreq.URL.Host = r.Host
	setForwardedHeaders(outreq, r)
	if err := outreq.Write(stream); err != nil {
		s.writeErrorPage(w, r, http.StatusBadGateway, pageAgentUnreachable)
		return
	}

	br := bufio.NewReader(stream)
	resp, err := http.ReadResponse(br, outreq)
	if err != nil {
		s.log.Warn("upgrade: read response", "host", r.Host, "error", err)
		s.writeErrorPage(w, r, http.StatusBadGateway, pageAgentUnreachable)
		return
	}

	// A non-101 response is an ordinary reply (a 404 or an auth failure); relay
	// it normally rather than hijacking.
	if resp.StatusCode != http.StatusSwitchingProtocols {
		defer resp.Body.Close()
		copyHeader(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return
	}

	clientConn, clientBuf, err := hj.Hijack()
	if err != nil {
		s.log.Warn("upgrade: hijack", "host", r.Host, "error", err)
		return
	}
	defer clientConn.Close()

	if err := resp.Write(clientBuf); err != nil {
		return
	}
	if err := clientBuf.Flush(); err != nil {
		return
	}

	// Splice both directions and finish as soon as either side goes away.
	done := make(chan struct{}, 2)
	go func() {
		// clientBuf may hold bytes the client pipelined after the handshake.
		io.Copy(stream, clientBuf)
		done <- struct{}{}
	}()
	go func() {
		io.Copy(clientConn, br)
		done <- struct{}{}
	}()
	<-done
}

// setForwardedHeaders applies the same forwarding headers the ReverseProxy
// path sets, for requests that bypass it.
func setForwardedHeaders(out, in *http.Request) {
	ip := clientIP(in)
	if prior := in.Header.Get("X-Forwarded-For"); prior != "" && ip != "" {
		out.Header.Set("X-Forwarded-For", prior+", "+ip)
	} else if ip != "" {
		out.Header.Set("X-Forwarded-For", ip)
	}
	out.Header.Set("X-Forwarded-Proto", "https")
	out.Header.Set("X-Forwarded-Host", in.Host)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}

func copyHeader(dst, src http.Header) {
	for k, vs := range src {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}
