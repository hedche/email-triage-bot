// Package decisionlog writes and reads the Decision Log: every Decision from
// one Triage Run, with what each Message looked like when it was scanned, so a
// person can Review it and Replay can re-evaluate it offline. Message bodies
// are never written.
package decisionlog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hedche/email-triage-bot/internal/triage"
)

// MessageSummary is what a Message looked like when it was scanned. It holds
// only what a Review or a Replay needs: never the body.
type MessageSummary struct {
	ID       string         `json:"id"`
	Account  string         `json:"account"`
	From     triage.Address `json:"from"`
	Subject  string         `json:"subject"`
	Snippet  string         `json:"snippet"`
	Received time.Time      `json:"received"`
}

// Record is one line of the Decision Log.
type Record struct {
	Decision triage.Decision `json:"decision"`
	Message  MessageSummary  `json:"message"`
}

// Summarise reduces a scanned Message to what the Decision Log keeps.
func Summarise(m triage.Message) MessageSummary {
	return MessageSummary{
		ID:       m.ID,
		Account:  m.Account,
		From:     m.From,
		Subject:  m.Subject,
		Snippet:  m.Snippet,
		Received: m.Received,
	}
}

// Writer appends Records to a Decision Log file, one JSON object per line.
// It is safe for concurrent use.
type Writer struct {
	mu   sync.Mutex
	f    *os.File
	bw   *bufio.Writer
	enc  *json.Encoder
	done bool
}

// NewWriter creates (or truncates) the Decision Log at path, creating the
// parent directory if it does not exist.
func NewWriter(path string) (*Writer, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create decision log directory: %w", err)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create decision log: %w", err)
	}
	bw := bufio.NewWriter(f)
	enc := json.NewEncoder(bw)
	return &Writer{f: f, bw: bw, enc: enc}, nil
}

// Append writes one Record and flushes it, so a Decision Log survives a crash
// mid-run.
func (w *Writer) Append(rec Record) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done {
		return fmt.Errorf("decision log: writer is closed")
	}
	if err := w.enc.Encode(rec); err != nil {
		return fmt.Errorf("write decision log record: %w", err)
	}
	if err := w.bw.Flush(); err != nil {
		return fmt.Errorf("flush decision log: %w", err)
	}
	return nil
}

// Close flushes and closes the Decision Log. It is safe to call twice.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done {
		return nil
	}
	w.done = true
	if err := w.bw.Flush(); err != nil {
		w.f.Close()
		return fmt.Errorf("flush decision log: %w", err)
	}
	if err := w.f.Close(); err != nil {
		return fmt.Errorf("close decision log: %w", err)
	}
	return nil
}

// Path is the file the Decision Log is being written to.
func (w *Writer) Path() string { return w.f.Name() }

// Read loads a whole Decision Log. Blank lines are skipped; a malformed line
// is an error naming its line number.
func Read(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open decision log: %w", err)
	}
	defer f.Close()
	return ReadFrom(f)
}

// ReadFrom loads a Decision Log from any reader.
func ReadFrom(r io.Reader) ([]Record, error) {
	var records []Record
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		raw := sc.Bytes()
		if len(trimSpace(raw)) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil, fmt.Errorf("decision log line %d: %w", line, err)
		}
		records = append(records, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read decision log: %w", err)
	}
	return records, nil
}

func trimSpace(b []byte) []byte {
	start := 0
	for start < len(b) && (b[start] == ' ' || b[start] == '\t' || b[start] == '\r' || b[start] == '\n') {
		start++
	}
	end := len(b)
	for end > start && (b[end-1] == ' ' || b[end-1] == '\t' || b[end-1] == '\r' || b[end-1] == '\n') {
		end--
	}
	return b[start:end]
}
