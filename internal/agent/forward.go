package agent

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"tunnel/internal/proto"
)

// MaxCapturedBody bounds how much of a body the inspector keeps per direction.
// Anything larger still passes through untouched; only the captured copy stops.
const MaxCapturedBody = 1 << 20 // 1 MiB

// Exchange is one request/response pair as observed by the agent.
//
// An Exchange is delivered to the Observer twice: once when the response
// headers arrive, and again when the body finishes. Observers must upsert by
// ID, which is what lets a long-lived SSE response show up in the dashboard
// while it is still streaming.
type Exchange struct {
	ID         string
	StartedAt  time.Time
	Duration   time.Duration
	Complete   bool
	RemoteAddr string
	Upgrade    bool

	Method string
	Path   string
	Proto  string

	ReqHeader        http.Header
	ReqBody          []byte
	ReqBodySize      int64
	ReqBodyTruncated bool

	Status            int
	RespHeader        http.Header
	RespBody          []byte
	RespBodySize      int64
	RespBodyTruncated bool

	Err string
}

// hostFor applies the configured Host header policy.
func (a *Agent) hostFor(original string) string {
	if a.cfg.HostHeaderValue != "" {
		return a.cfg.HostHeaderValue
	}
	if a.cfg.HostHeader == HostRewrite {
		return a.cfg.LocalAddr
	}
	return original
}

// forward relays one HTTP exchange to the local service.
//
// It speaks HTTP/1.1 over a raw TCP connection rather than going through
// http.Client, because the tunnel has to carry whatever the local server sends
// -- including protocol upgrades and streaming responses -- without a transport
// layer normalising it.
func (s *session) forward(stream net.Conn, br *bufio.Reader, hdr *proto.StreamHeader) error {
	start := time.Now()
	req, err := http.ReadRequest(br)
	if err != nil {
		return fmt.Errorf("read request: %w", err)
	}
	defer req.Body.Close()

	ex := &Exchange{
		ID:         hdr.RequestID,
		StartedAt:  start,
		RemoteAddr: hdr.RemoteAddr,
		Upgrade:    hdr.Upgrade,
		Method:     req.Method,
		Path:       req.URL.RequestURI(),
		Proto:      req.Proto,
		ReqHeader:  req.Header.Clone(),
	}
	req.Host = s.agent.hostFor(req.Host)
	ex.ReqHeader.Set("Host", req.Host)

	fail := func(err error) error {
		ex.Err = err.Error()
		ex.Complete = true
		ex.Duration = time.Since(start)
		s.emit(ex)
		return err
	}

	local, err := net.DialTimeout("tcp", s.agent.cfg.LocalAddr, 10*time.Second)
	if err != nil {
		// Closing the stream without a response makes the server render its
		// branded "agent unreachable" page, which already tells the visitor to
		// check that the local server is running.
		return fail(fmt.Errorf("dial local %s: %w", s.agent.cfg.LocalAddr, err))
	}
	defer local.Close()

	reqCapture := newCapture(req.Body)
	req.Body = io.NopCloser(reqCapture)

	// Write the request from its own goroutine while reading the response here.
	// A local server that answers before draining the body (a 413, or any
	// upgrade handshake) would otherwise deadlock against its own socket buffer.
	writeDone := make(chan error, 1)
	go func() { writeDone <- req.Write(local) }()

	localBr := bufio.NewReader(local)
	resp, err := http.ReadResponse(localBr, req)
	if err != nil {
		if werr := <-writeDone; werr != nil {
			return fail(fmt.Errorf("write request to local service: %w", werr))
		}
		return fail(fmt.Errorf("read response from local service: %w", err))
	}
	defer resp.Body.Close()

	ex.ReqBody, ex.ReqBodySize, ex.ReqBodyTruncated = reqCapture.result()
	ex.Status = resp.StatusCode
	ex.RespHeader = resp.Header.Clone()
	ex.Duration = time.Since(start)

	select {
	case werr := <-writeDone:
		if werr != nil {
			s.log.Warn("write request to local service failed", "error", werr)
			if ex.Err == "" {
				ex.Err = werr.Error()
			}
		}
	default:
	}

	s.emit(ex) // headers known; body may still be streaming

	if resp.StatusCode == http.StatusSwitchingProtocols {
		err := s.spliceUpgrade(stream, br, local, localBr, resp)
		ex.Complete = true
		ex.Duration = time.Since(start)
		if err != nil && !isBenignClose(err) {
			ex.Err = err.Error()
		}
		s.emit(ex)
		return err
	}

	respCapture := newCapture(resp.Body)
	resp.Body = io.NopCloser(respCapture)
	err = resp.Write(stream)

	ex.RespBody, ex.RespBodySize, ex.RespBodyTruncated = respCapture.result()
	ex.Complete = true
	ex.Duration = time.Since(start)
	if err != nil && !isBenignClose(err) {
		ex.Err = err.Error()
	}
	s.emit(ex)
	return err
}

// spliceUpgrade relays the 101 response and then pipes the two connections
// together until either end closes.
func (s *session) spliceUpgrade(stream net.Conn, streamBr *bufio.Reader, local net.Conn, localBr *bufio.Reader, resp *http.Response) error {
	if err := resp.Write(stream); err != nil {
		return fmt.Errorf("write upgrade response: %w", err)
	}
	done := make(chan error, 2)
	go func() {
		_, err := io.Copy(local, streamBr)
		// Let the local service see the half-close so it can finish cleanly.
		if c, ok := local.(*net.TCPConn); ok {
			c.CloseWrite()
		}
		done <- err
	}()
	go func() {
		_, err := io.Copy(stream, localBr)
		done <- err
	}()
	return <-done
}

// emit delivers an exchange to the observer, if one is configured.
func (s *session) emit(ex *Exchange) {
	if o := s.agent.cfg.Observer; o != nil {
		o.Exchange(ex)
	}
}

// capture copies up to MaxCapturedBody bytes of a stream while counting all of
// it, so the inspector can show a body without buffering an arbitrarily large
// upload or holding up the proxy.
type capture struct {
	sync.Mutex
	r     io.Reader
	buf   []byte
	total int64
}

func newCapture(r io.Reader) *capture {
	return &capture{r: r, buf: make([]byte, 0, 512)}
}

func (c *capture) Read(p []byte) (int, error) {
	c.Lock()
	defer c.Unlock()
	n, err := c.r.Read(p)
	if n > 0 {
		c.total += int64(n)
		if room := MaxCapturedBody - len(c.buf); room > 0 {
			c.buf = append(c.buf, p[:min(n, room)]...)
		}
	}
	return n, err
}

// result returns the captured prefix, the true size, and whether it was cut off.
func (c *capture) result() ([]byte, int64, bool) {
	c.Lock()
	defer c.Unlock()
	return c.buf, c.total, c.total > int64(len(c.buf))
}
