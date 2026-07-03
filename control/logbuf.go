package control

import (
	"bytes"
	"strings"
	"sync"
	"time"
)

// defaultLogCapacity is the number of log lines the ring buffer retains. Older
// lines are evicted as new ones arrive; a front-end that has fallen behind by
// more than this many lines simply sees a gap (it always gets the most recent
// capacity lines, oldest-first).
const defaultLogCapacity = 2000

// LogBuffer is a small fixed-capacity ring buffer of log lines, safe for
// concurrent use. It lives in this package (rather than cmd/veil-service or
// wintunnel) because it is part of the control-channel contract: the Seq
// cursor it hands out is the same Since cursor Request/Response carry over
// the wire, so keeping the type next to LogLine keeps that relationship
// obvious and avoids a needless extra package for ~80 lines of code.
//
// A LogBuffer implements io.Writer so it can be plugged straight into
// log.SetOutput (typically via io.MultiWriter alongside os.Stderr), which
// means every existing log.Printf call site across the engine/service code
// gets captured for free — no call-site changes required. Each Write call is
// split on newlines and stored as one or more LogLine entries.
//
// The buffer is created once at service startup and lives for the process
// lifetime, so logs span tunnel restarts rather than resetting on every
// Connect/Disconnect cycle.
type LogBuffer struct {
	mu       sync.Mutex
	cap      int
	lines    []LogLine
	nextSeq  uint64 // 1-based: 0 is reserved as the "beginning of time" cursor
	overflow uint64 // count of lines evicted, for future diagnostics (unused today)
}

// NewLogBuffer creates a ring buffer holding up to capacity lines. A
// non-positive capacity falls back to defaultLogCapacity.
func NewLogBuffer(capacity int) *LogBuffer {
	if capacity <= 0 {
		capacity = defaultLogCapacity
	}
	return &LogBuffer{cap: capacity, lines: make([]LogLine, 0, capacity), nextSeq: 1}
}

// Write implements io.Writer. It splits p on newlines and appends each
// non-empty line to the buffer. Partial lines (no trailing newline) are still
// appended as-is, since log.Logger always writes complete, newline-terminated
// records in a single Write call.
func (b *LogBuffer) Write(p []byte) (int, error) {
	n := len(p)
	for _, raw := range bytes.Split(bytes.TrimRight(p, "\n"), []byte("\n")) {
		line := strings.TrimRight(string(raw), "\r")
		if line == "" {
			continue
		}
		b.Append(line)
	}
	return n, nil
}

// Append adds one pre-formatted line to the buffer, tagging it with the next
// sequence number and the current time. Level is left empty; the standard
// log package does not distinguish levels, and callers that want one can
// prefix msg themselves (e.g. "warning: ...", which several call sites in
// wintunnel already do).
func (b *LogBuffer) Append(msg string) LogLine {
	b.mu.Lock()
	defer b.mu.Unlock()

	line := LogLine{Seq: b.nextSeq, Time: time.Now().Unix(), Msg: msg}
	b.nextSeq++

	if len(b.lines) >= b.cap {
		// Evict oldest. Shifting is O(n) but capacity is small (2000) and
		// this only runs once per log line, which is already a rare,
		// human-scale event compared to data-plane traffic.
		copy(b.lines, b.lines[1:])
		b.lines = b.lines[:len(b.lines)-1]
		b.overflow++
	}
	b.lines = append(b.lines, line)
	return line
}

// Since returns every retained line with Seq > since, oldest first. Sequence
// numbers are 1-based, so passing since=0 returns the whole retained backlog
// (up to capacity) — 0 is never a valid line's Seq.
func (b *LogBuffer) Since(since uint64) []LogLine {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Lines are always appended in increasing Seq order, so a binary-search-
	// free linear scan from the front is fine at this size; find the first
	// line with Seq > since.
	start := 0
	for start < len(b.lines) && b.lines[start].Seq <= since {
		start++
	}
	out := make([]LogLine, len(b.lines)-start)
	copy(out, b.lines[start:])
	return out
}
