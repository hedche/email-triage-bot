package backfill

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/hedche/email-triage-bot/internal/archive"
	"github.com/hedche/email-triage-bot/internal/decisionlog"
	"github.com/hedche/email-triage-bot/internal/triage"
)

type memStore struct {
	objects map[string][]byte
	puts    int
}

func (m *memStore) Put(_ context.Context, key string, body []byte, _ string) error {
	m.objects[key] = body
	m.puts++
	return nil
}

func (m *memStore) Get(_ context.Context, key string) ([]byte, error) {
	b, ok := m.objects[key]
	if !ok {
		return nil, archive.ErrNotFound
	}
	return b, nil
}

func logs(scanned int) string {
	var b strings.Builder
	b.WriteString("not json\n")
	for i := range 2 {
		id := fmt.Sprintf("m%d", i)
		fmt.Fprintf(&b, `{"time":"2026-11-08T05:00:50Z","msg":"decision","run_id":"r1","account":"A@x.test","message_id":%q,"tier":"none","verdict":"leave","action":"none","from":"s@y.test","from_name":"S","subject":"hi"}`+"\n", id)
	}
	fmt.Fprintf(&b, `{"time":"2026-11-08T05:00:51Z","msg":"run summary","run_id":"r1","scanned":%d,"left":2,"duration_ms":1500,"accounts":[{"account":"A@x.test","scanned":%d}]}`+"\n", scanned, scanned)
	return b.String()
}

func options(store archive.ReadStore) Options {
	return Options{
		Before: time.Date(2026, 11, 8, 12, 50, 0, 0, time.UTC),
		Source: "pod-logs",
		Store:  store,
		Log:    slog.New(slog.DiscardHandler),
		Lookup: func(_ context.Context, _, id string) (string, error) {
			if id == "m1" {
				return "", nil
			}
			return "<abc@x.test>", nil
		},
	}
}

func TestExecuteWritesMarkedRecordsWithRefs(t *testing.T) {
	runs, err := Parse(strings.NewReader(logs(2) + logs(2))) // overlapping sources
	if err != nil {
		t.Fatal(err)
	}
	store := &memStore{objects: map[string][]byte{}}
	res, err := Execute(context.Background(), runs, options(store))
	if err != nil {
		t.Fatal(err)
	}
	if res.RunsWritten != 1 || res.RecordsWritten != 2 || res.RecordsNoRef != 1 {
		t.Fatalf("result = %+v", res)
	}
	zr, err := gzip.NewReader(bytes.NewReader(store.objects["runs/2026/11/08/r1.jsonl.gz"]))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(zr)
	records, err := decisionlog.ReadFrom(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := triage.Message{Account: "A@x.test", RFCMessageID: "<abc@x.test>"}.Ref()
	if got := records[0]; got.Message.Ref != want || got.Message.RFCMessageID != "<abc@x.test>" || !got.Backfilled || got.Source != "pod-logs" {
		t.Errorf("record 0 = %+v", got)
	}
	if got := records[1]; got.Message.Ref != "" || got.Message.RFCMessageID != "" {
		t.Errorf("record without a Message-ID must have no ref: %+v", got)
	}
	var sum map[string]any
	if err := json.Unmarshal(store.objects["runs/2026/11/08/r1.summary.json"], &sum); err != nil {
		t.Fatal(err)
	}
	if sum["backfilled"] != true || sum["source"] != "pod-logs" || sum["RunID"] != "r1" || sum["Duration"] != float64(1500*time.Millisecond) {
		t.Errorf("summary = %v", sum)
	}
}

func TestExecuteNeverOverwrites(t *testing.T) {
	runs, _ := Parse(strings.NewReader(logs(2)))
	store := &memStore{objects: map[string][]byte{}}
	if _, err := Execute(context.Background(), runs, options(store)); err != nil {
		t.Fatal(err)
	}
	before := store.puts
	res, err := Execute(context.Background(), runs, options(store))
	if err != nil {
		t.Fatal(err)
	}
	if store.puts != before || res.RunsExisting != 1 || res.RunsWritten != 0 {
		t.Errorf("puts %d->%d, result %+v", before, store.puts, res)
	}
}

func TestExecuteSkipsIncompleteLateAndDryRuns(t *testing.T) {
	store := &memStore{objects: map[string][]byte{}}

	runs, _ := Parse(strings.NewReader(logs(3)))
	res, err := Execute(context.Background(), runs, options(store))
	if err != nil || res.RunsIncomplete != 1 || store.puts != 0 {
		t.Errorf("incomplete: res %+v err %v puts %d", res, err, store.puts)
	}

	runs, _ = Parse(strings.NewReader(logs(2)))
	opts := options(store)
	opts.Before = time.Date(2026, 11, 8, 5, 0, 0, 0, time.UTC)
	res, err = Execute(context.Background(), runs, opts)
	if err != nil || res.RunsTooLate != 1 || store.puts != 0 {
		t.Errorf("late: res %+v err %v puts %d", res, err, store.puts)
	}

	opts = options(store)
	opts.DryRun = true
	if _, err = Execute(context.Background(), runs, opts); err != nil || store.puts != 0 {
		t.Errorf("dry run wrote: err %v puts %d", err, store.puts)
	}
}
