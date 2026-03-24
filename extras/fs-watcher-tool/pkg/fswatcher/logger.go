package fswatcher

import (
	"encoding/json"
	"io"
	"sync"
)

// Logger writes NDJSON-formatted events to an output writer.
type Logger struct {
	mu  sync.Mutex
	enc *json.Encoder
}

// NewLogger creates a Logger that writes to the given writer.
func NewLogger(w io.Writer) *Logger {
	return &Logger{enc: json.NewEncoder(w)}
}

// Write serialises a single event as one NDJSON line.
func (l *Logger) Write(ev Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.enc.Encode(ev)
}
