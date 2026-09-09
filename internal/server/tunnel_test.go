package server_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"tunnel/internal/agent"
	"tunnel/internal/server"
)

const testDomain = "tl.test"

// harness wires a server, an agent and a local target together over loopback,
// so tests exercise the real protocol rather than a mock of it.
type harness struct {
	t         *testing.T
	srv       *server.Server
	dataAddr  string
	label     string
	targetURL string
}

// newHarness starts everything and blocks until the tunnel is live.
func newHarness(t *testing.T, target http.Handler, cfg agent.Config) *harness {
	t.Helper()

	local := httptest.NewServer(target)
	t.Cleanup(local.Close)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := server.New(server.Config{
		Domain:          testDomain,
		Auth:            server.OpenAuth{},
		DisconnectLease: 2 * time.Second,
		PublicScheme:    "http",
		Logger:          logger,
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	ctlLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen control: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go srv.ServeControl(ctx, ctlLn)

	dataSrv := httptest.NewServer(srv.Handler())
	t.Cleanup(dataSrv.Close)

	cfg.ServerAddr = ctlLn.Addr().String()
	if cfg.LocalAddr == "" {
		cfg.LocalAddr = strings.TrimPrefix(local.URL, "http://")
	}
	cfg.TLSDisabled = true
	cfg.Logger = logger

	ag, err := agent.New(cfg)
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	agentDone := make(chan error, 1)
	go func() { agentDone <- ag.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-agentDone:
		case <-time.After(5 * time.Second):
			t.Error("agent did not stop within 5s")
		}
	})

	h := &harness{
		t:         t,
		srv:       srv,
		dataAddr:  strings.TrimPrefix(dataSrv.URL, "http://"),
		targetURL: local.URL,
	}
	h.label = waitForTunnel(t, ag, agentDone)
	return h
}

// waitForTunnel blocks until the agent reports a public URL.
func waitForTunnel(t *testing.T, ag *agent.Agent, agentDone <-chan error) string {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		if u := ag.URL(); u != "" {
			host := strings.TrimPrefix(u, "http://")
			label, _, ok := strings.Cut(host, ".")
			if !ok {
				t.Fatalf("tunnel URL %q has no label", u)
			}
			return label
		}
		select {
		case err := <-agentDone:
			t.Fatalf("agent stopped before the tunnel opened: %v", err)
		case <-deadline:
			t.Fatal("tunnel did not open within 10s")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// do sends a request through the tunnel by aiming it at the data-plane
// listener while setting the tunnel hostname as Host.
func (h *harness) do(req *http.Request) *http.Response {
	h.t.Helper()
	resp, err := h.roundTrip(req, h.label)
	if err != nil {
		h.t.Fatalf("request through tunnel: %v", err)
	}
	return resp
}

func (h *harness) roundTrip(req *http.Request, label string) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = h.dataAddr
	req.Host = label + "." + testDomain
	// No redirect following and no connection reuse across subtests, so each
	// assertion sees exactly the exchange it made.
	c := &http.Client{
		Transport:     &http.Transport{DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       30 * time.Second,
	}
	return c.Do(req)
}

func mustGet(t *testing.T, h *harness, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://placeholder"+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Accept", "text/html")
	return h.do(req)
}

func TestForwardsGetAndPreservesHeaders(t *testing.T) {
	var gotHost, gotFwdProto, gotFwdHost, gotFwdFor, gotCustom string
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		gotFwdProto = r.Header.Get("X-Forwarded-Proto")
		gotFwdHost = r.Header.Get("X-Forwarded-Host")
		gotFwdFor = r.Header.Get("X-Forwarded-For")
		gotCustom = r.Header.Get("X-Custom")
		w.Header().Set("X-Response-Marker", "from-local")
		fmt.Fprintf(w, "hello from %s", r.URL.Path)
	}), agent.Config{})

	req, _ := http.NewRequest(http.MethodGet, "http://placeholder/some/path?q=1", nil)
	req.Header.Set("X-Custom", "kept")
	resp := h.do(req)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if got, want := string(body), "hello from /some/path"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Response-Marker"); got != "from-local" {
		t.Errorf("response header lost: X-Response-Marker = %q", got)
	}
	if gotCustom != "kept" {
		t.Errorf("request header lost: X-Custom = %q", gotCustom)
	}

	// Default policy is preserve, so the local service sees the public name.
	wantHost := h.label + "." + testDomain
	if gotHost != wantHost {
		t.Errorf("Host = %q, want %q (preserve is the default)", gotHost, wantHost)
	}
	if gotFwdProto != "https" {
		t.Errorf("X-Forwarded-Proto = %q, want https", gotFwdProto)
	}
	if gotFwdHost != wantHost {
		t.Errorf("X-Forwarded-Host = %q, want %q", gotFwdHost, wantHost)
	}
	if gotFwdFor == "" {
		t.Error("X-Forwarded-For was not set")
	}
}

func TestHostHeaderRewrite(t *testing.T) {
	var gotHost string
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
	}), agent.Config{HostHeader: agent.HostRewrite})

	resp := mustGet(t, h, "/")
	resp.Body.Close()

	if strings.HasSuffix(gotHost, testDomain) {
		t.Errorf("Host = %q, want the local address (rewrite mode)", gotHost)
	}
	if gotHost != strings.TrimPrefix(h.targetURL, "http://") {
		t.Errorf("Host = %q, want %q", gotHost, strings.TrimPrefix(h.targetURL, "http://"))
	}
}

func TestLargeBodyRoundTrip(t *testing.T) {
	// Larger than the 1 MiB inspector capture and than yamux's default window,
	// so this exercises flow control in both directions rather than a single
	// buffered write.
	const size = 4 << 20
	payload := make([]byte, size)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("generate payload: %v", err)
	}

	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("request body differed: got %d bytes, want %d", len(got), len(payload))
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write(got) // echo it straight back
	}), agent.Config{})

	req, _ := http.NewRequest(http.MethodPost, "http://placeholder/upload", bytes.NewReader(payload))
	req.ContentLength = size
	resp := h.do(req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("response body differed: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestStreamingResponseIsNotBuffered(t *testing.T) {
	release := make(chan struct{})
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-release // hold the response open
		fmt.Fprint(w, "data: second\n\n")
		w.(http.Flusher).Flush()
	}), agent.Config{})
	defer close(release)

	req, _ := http.NewRequest(http.MethodGet, "http://placeholder/events", nil)
	resp := h.do(req)
	defer resp.Body.Close()

	// The first chunk must arrive while the handler is still running. If any
	// hop buffered the whole response, this read would block until close(release)
	// which happens after the test returns, and the deadline would fire.
	type readResult struct {
		line string
		err  error
	}
	got := make(chan readResult, 1)
	go func() {
		line, err := bufio.NewReader(resp.Body).ReadString('\n')
		got <- readResult{line, err}
	}()

	select {
	case r := <-got:
		if r.err != nil {
			t.Fatalf("read first chunk: %v", r.err)
		}
		if want := "data: first\n"; r.line != want {
			t.Errorf("first chunk = %q, want %q", r.line, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first chunk did not arrive within 5s; the response was buffered")
	}
}

func TestUnknownSubdomainReturns404(t *testing.T) {
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("local service should not have been reached")
	}), agent.Config{})

	req, _ := http.NewRequest(http.MethodGet, "http://placeholder/", nil)
	req.Header.Set("Accept", "text/html")
	resp, err := h.roundTrip(req, "nobody-home-0000")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Tunnel not found") {
		t.Errorf("expected the branded not-found page, got: %s", truncate(string(body), 200))
	}
}

func TestNonTunnelHostReturns404(t *testing.T) {
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), agent.Config{})

	req, _ := http.NewRequest(http.MethodGet, "http://"+h.dataAddr+"/", nil)
	req.Header.Set("Accept", "text/html")
	req.Host = "somewhere.else.example"
	c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Unknown address") {
		t.Errorf("expected the unknown-address page, got: %s", truncate(string(body), 200))
	}
}

func TestLocalServiceDownReturns502(t *testing.T) {
	// Bind a port and release it, so the agent has a plausible address that
	// nothing is listening on.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadAddr := strings.TrimPrefix(dead.URL, "http://")
	dead.Close()

	h := newHarness(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		agent.Config{LocalAddr: deadAddr})

	resp := mustGet(t, h, "/")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Agent unreachable") {
		t.Errorf("expected the branded 502 page, got: %s", truncate(string(body), 200))
	}
}

func TestConcurrentRequests(t *testing.T) {
	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond) // force overlap
		fmt.Fprint(w, r.URL.Path)
	}), agent.Config{})

	const n = 40
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := fmt.Sprintf("/req/%d", i)
			req, _ := http.NewRequest(http.MethodGet, "http://placeholder"+path, nil)
			resp, err := h.roundTrip(req, h.label)
			if err != nil {
				errs <- fmt.Errorf("request %d: %w", i, err)
				return
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if string(body) != path {
				errs <- fmt.Errorf("request %d: body = %q, want %q", i, body, path)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
