package agent

import (
	"log"
	"log/slog"
	"math/rand"
	"strings"
	"time"
)

// backoff produces reconnect delays: exponential growth with jitter.
//
// The ceiling is deliberately low. This is a development tool, and the common
// outage is the user restarting their own tunnel server or resuming a laptop;
// making them wait half a minute for the URL to come back would be worse than
// the load a few extra retries put on the server. Jitter keeps a fleet of
// agents from reconnecting in lockstep after a shared restart.
type backoff struct {
	cur time.Duration
}

const (
	backoffMin = 500 * time.Millisecond
	backoffMax = 8 * time.Second
)

func newBackoff() *backoff { return &backoff{} }

func (b *backoff) next() time.Duration {
	if b.cur == 0 {
		b.cur = backoffMin
	} else {
		b.cur *= 2
		if b.cur > backoffMax {
			b.cur = backoffMax
		}
	}
	// Full jitter over the current ceiling. Without it, every agent that lost
	// the same server restart would come back in lockstep.
	return backoffMin + time.Duration(rand.Int63n(int64(b.cur)))
}

// reset returns to the minimum delay after a successful connection.
func (b *backoff) reset() { b.cur = 0 }

// newYamuxLogger adapts slog to the *log.Logger yamux expects.
func newYamuxLogger(l *slog.Logger) *log.Logger {
	return log.New(slogWriter{l}, "", 0)
}

type slogWriter struct{ l *slog.Logger }

func (w slogWriter) Write(p []byte) (int, error) {
	if msg := strings.TrimSpace(string(p)); msg != "" {
		w.l.Debug(msg, "component", "yamux")
	}
	return len(p), nil
}
