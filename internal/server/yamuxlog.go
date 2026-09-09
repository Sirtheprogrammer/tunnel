package server

import (
	"log"
	"log/slog"
	"strings"
)

// newYamuxLogger adapts slog to the *log.Logger yamux expects. yamux logs
// routine stream churn and peer disconnects, which are noise at our level, so
// everything lands at debug.
func newYamuxLogger(l *slog.Logger) *log.Logger {
	return log.New(slogWriter{l}, "", 0)
}

type slogWriter struct{ l *slog.Logger }

func (w slogWriter) Write(p []byte) (int, error) {
	msg := strings.TrimSpace(string(p))
	if msg != "" {
		w.l.Debug(msg, "component", "yamux")
	}
	return len(p), nil
}
