package main

import (
	"fmt"
	"io"
	"sync"
	"time"

	"tunnel/internal/agent"
)

// console is the plain-text traffic view: a banner when the tunnel opens, then
// one line per request. The full bubbletea dashboard replaces this for
// interactive terminals in a later milestone; this stays as the fallback for
// pipes, CI and non-TTY output, where a repainting UI would be unreadable.
type console struct {
	w     io.Writer
	local string

	mu   sync.Mutex
	last agent.State
}

func newConsole(w io.Writer, local string) *console {
	return &console{w: w, local: local}
}

// StateChanged announces transitions only. A long outage produces one
// StateReconnecting per retry, and reprinting the same line every few seconds
// would bury the request log underneath it.
func (c *console) StateChanged(s agent.State) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s == c.last {
		return
	}
	c.last = s
	switch s {
	case agent.StateConnecting:
		fmt.Fprintln(c.w, "Connecting...")
	case agent.StateReconnecting:
		fmt.Fprintln(c.w, "Connection lost, reconnecting...")
	}
}

func (c *console) TunnelOpened(url string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fmt.Fprintf(c.w, "\nForwarding  %s  ->  http://%s\n", url, c.local)
	fmt.Fprintf(c.w, "Press Ctrl-C to stop\n\n")
}

// Exchange prints one line per request. An exchange is reported twice -- once
// when the response headers arrive and once when the body finishes -- and only
// the completed form is printed, so the duration covers the whole exchange.
func (c *console) Exchange(ex *agent.Exchange) {
	if !ex.Complete {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	status := fmt.Sprintf("%d", ex.Status)
	if ex.Err != "" && ex.Status == 0 {
		status = "ERR"
	}
	fmt.Fprintf(c.w, "%s  %-6s %-3s %-40s %8s\n",
		ex.StartedAt.Format("15:04:05"),
		ex.Method,
		status,
		truncatePath(ex.Path, 40),
		ex.Duration.Round(time.Millisecond),
	)
	if ex.Err != "" {
		fmt.Fprintf(c.w, "          %s\n", ex.Err)
	}
}

func truncatePath(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}
