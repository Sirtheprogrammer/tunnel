package server_test

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"tunnel/internal/agent"
)

// echoUpgradeHandler answers a protocol upgrade with 101 and then echoes every
// line back with an "echo: " prefix. It stands in for a WebSocket server: the
// tunnel only has to carry the handshake and then an opaque bidirectional byte
// stream, which is exactly what this exercises without pulling in a WebSocket
// library.
func echoUpgradeHandler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "echo") {
			http.Error(w, "expected an echo upgrade", http.StatusBadRequest)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("local test server does not support hijacking")
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()

		fmt.Fprint(buf, "HTTP/1.1 101 Switching Protocols\r\n"+
			"Upgrade: echo\r\nConnection: Upgrade\r\nX-Echo-Ready: 1\r\n\r\n")
		if err := buf.Flush(); err != nil {
			return
		}
		for {
			line, err := buf.ReadString('\n')
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(buf, "echo: %s", line); err != nil {
				return
			}
			if err := buf.Flush(); err != nil {
				return
			}
		}
	})
}

func TestProtocolUpgradeIsSpliced(t *testing.T) {
	h := newHarness(t, echoUpgradeHandler(t), agent.Config{})

	conn, err := net.DialTimeout("tcp", h.dataAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial data plane: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}

	host := h.label + "." + testDomain
	fmt.Fprintf(conn, "GET /ws HTTP/1.1\r\nHost: %s\r\n"+
		"Upgrade: echo\r\nConnection: Upgrade\r\n\r\n", host)

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read upgrade response: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Echo-Ready"); got != "1" {
		t.Errorf("X-Echo-Ready = %q, want 1; upgrade headers were not relayed", got)
	}

	// Several round trips, to prove the splice stays open rather than carrying
	// a single message.
	for i := 0; i < 3; i++ {
		msg := fmt.Sprintf("hello-%d\n", i)
		if _, err := conn.Write([]byte(msg)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		got, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if want := "echo: " + msg; got != want {
			t.Fatalf("round trip %d = %q, want %q", i, got, want)
		}
	}
}

func TestUpgradeRejectedByLocalServiceRelaysStatus(t *testing.T) {
	// The local service declines the upgrade with an ordinary response. The
	// server must relay it normally instead of hijacking the connection.
	h := newHarness(t, echoUpgradeHandler(t), agent.Config{})

	req, _ := http.NewRequest(http.MethodGet, "http://placeholder/ws", nil)
	req.Header.Set("Upgrade", "websocket") // not "echo", so the target refuses
	req.Header.Set("Connection", "Upgrade")
	resp := h.do(req)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 relayed from the local service", resp.StatusCode)
	}
}
