// Package logstream fans out the log lines of running deployments to any
// number of live viewers, replaying the history to late subscribers.
package logstream

import (
	"bytes"
	"strings"
	"sync"
	"time"
)

type Stream struct {
	mu     sync.Mutex
	lines  []string
	subs   map[chan string]struct{}
	closed bool
}

// Write appends one line and pushes it to subscribers. A subscriber too slow
// to keep up is disconnected; it can reconnect and get the full history.
func (s *Stream) Write(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.lines = append(s.lines, line)
	for ch := range s.subs {
		select {
		case ch <- line:
		default:
			delete(s.subs, ch)
			close(ch)
		}
	}
}

// Subscribe returns the lines written so far and a channel carrying the next
// ones. The channel is closed when the stream ends.
func (s *Stream) Subscribe() ([]string, <-chan string, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	history := append([]string(nil), s.lines...)
	ch := make(chan string, 512)
	if s.closed {
		close(ch)
		return history, ch, func() {}
	}
	s.subs[ch] = struct{}{}
	return history, ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.subs[ch]; ok {
			delete(s.subs, ch)
			close(ch)
		}
	}
}

func (s *Stream) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.lines, "\n")
}

func (s *Stream) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	for ch := range s.subs {
		close(ch)
	}
	s.subs = nil
}

type Hub struct {
	mu      sync.Mutex
	streams map[string]*Stream
}

func NewHub() *Hub { return &Hub{streams: map[string]*Stream{}} }

func (h *Hub) Open(id string) *Stream {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := &Stream{subs: map[chan string]struct{}{}}
	h.streams[id] = s
	return s
}

func (h *Hub) Get(id string) *Stream {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.streams[id]
}

// Close ends the stream. It stays readable for a short grace period so viewers
// that connect right at the end still find it, then the persisted log is used.
func (h *Hub) Close(id string) {
	h.mu.Lock()
	s := h.streams[id]
	h.mu.Unlock()
	if s == nil {
		return
	}
	s.close()
	time.AfterFunc(time.Minute, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.streams[id] == s {
			delete(h.streams, id)
		}
	})
}

// LineWriter is an io.Writer splitting its input into lines for a sink.
// Carriage returns (progress bars) are treated as line breaks.
type LineWriter struct {
	mu   sync.Mutex
	buf  []byte
	sink func(string)
}

func NewLineWriter(sink func(string)) *LineWriter { return &LineWriter{sink: sink} }

func (w *LineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexAny(w.buf, "\r\n")
		if i < 0 {
			break
		}
		if line := string(w.buf[:i]); line != "" {
			w.sink(line)
		}
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// Flush emits a trailing partial line, if any.
func (w *LineWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.sink(string(w.buf))
		w.buf = nil
	}
}
