package server_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"tunnel/internal/server"
)

func TestHealthz(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := server.New(server.Config{
		Domain:          "example.com",
		Auth:            server.OpenAuth{},
		DisconnectLease: time.Second,
		PublicScheme:    "http",
		Logger:          logger,
	})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	defer srv.Close()

	dataSrv := httptest.NewServer(srv.Handler())
	defer dataSrv.Close()

	req, err := http.NewRequest(http.MethodGet, dataSrv.URL+"/healthz", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	// Do not set Host header so it doesn't map to a tunnel, or if we do, it shouldn't matter 
	// for the healthz check if it intercepts it.
	
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}
