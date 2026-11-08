package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hedche/email-triage-bot/internal/archive"
	"github.com/hedche/email-triage-bot/internal/decisionlog"
	"github.com/hedche/email-triage-bot/internal/triage"
)

// fakeBucket serves ListObjectsV2 and GET for a bucket called b, and fails the
// test on anything that is not a read.
func fakeBucket(t *testing.T, objects map[string][]byte) archive.Reader {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("search made a %s request", r.Method)
			http.Error(w, "no", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/b" {
			type content struct{ Key string }
			var page struct {
				XMLName  xml.Name `xml:"ListBucketResult"`
				Contents []content
			}
			for k := range objects {
				if strings.HasPrefix(k, r.URL.Query().Get("prefix")) {
					page.Contents = append(page.Contents, content{k})
				}
			}
			xml.NewEncoder(w).Encode(page)
			return
		}
		body, ok := objects[strings.TrimPrefix(r.URL.Path, "/b/")]
		if !ok {
			http.Error(w, "NoSuchKey", http.StatusNotFound)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	return archive.NewReader(archive.Config{Host: host, Port: port, Bucket: "b", AccessKey: "AK", SecretKey: "SK"})
}

var searchNow = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

func TestSearchEndToEndAgainstAFakeBucket(t *testing.T) {
	body, err := archive.GzipJSONL([]decisionlog.Record{
		{
			Decision: triage.Decision{RunID: "20261105T050000Z-aa", Verdict: triage.VerdictMarkRead, Tier: triage.TierSender, Rule: "domain:example.com"},
			Message:  decisionlog.MessageSummary{Ref: "aaaaaaaaaaaa", Account: "owner@example.org", From: triage.Address{Email: "a@example.com"}, Subject: "Hello"},
		},
		{
			Decision:   triage.Decision{RunID: "20261105T050000Z-aa", Verdict: triage.VerdictLeave, Tier: triage.TierNone},
			Message:    decisionlog.MessageSummary{Account: "owner@example.org", From: triage.Address{Email: "b@example.net"}, Subject: "Other"},
			Backfilled: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := fakeBucket(t, map[string][]byte{"runs/2026/11/05/20261105T050000Z-aa.jsonl.gz": body})

	opts, err := parseSearch([]string{"--from", "example.com"}, searchNow, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := runSearch(context.Background(), r, opts, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	s := out.String()
	if !strings.Contains(s, "a@example.com") || strings.Contains(s, "b@example.net") || !strings.Contains(s, "Showing 1 of 1") {
		t.Errorf("table:\n%s", s)
	}

	opts, err = parseSearch([]string{"--json", "--verdict", "leave"}, searchNow, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := runSearch(context.Background(), r, opts, &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if lines := strings.Split(strings.TrimSpace(out.String()), "\n"); len(lines) != 1 || !strings.Contains(lines[0], `"backfilled":true`) || !strings.Contains(lines[0], `"no ref"`) {
		t.Errorf("json:\n%s", out.String())
	}
}

func TestSearchExitsNonZeroWhenAnObjectWasSkipped(t *testing.T) {
	r := fakeBucket(t, map[string][]byte{"runs/2026/11/05/x.jsonl.gz": []byte("not gzip")})
	opts, err := parseSearch(nil, searchNow, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := runSearch(context.Background(), r, opts, &out); code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
}

func TestParseSearchRejectsBadCommandLines(t *testing.T) {
	for _, args := range [][]string{
		{"--from", ""},
		{"--ref", "nope"},
		{"--verdict", "gone"},
		{"--since", "2026-11-05T00:00:00Z"},
		{"--since", "2026-11-05", "--until", "2026-11-01"},
		{"--limit", "0"},
		{"stray"},
		{"--commands", "--verdict", "leave"},
		{"--commands", "--ref", "aaaaaaaaaaaa"},
	} {
		if _, err := parseSearch(args, searchNow, io.Discard); err == nil {
			t.Errorf("%v should be rejected", args)
		}
	}
	opts, err := parseSearch(nil, searchNow, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if opts.query.Limit != 50 || opts.query.Since.Format("2006-01-02") != "2026-10-09" || opts.query.Until.Format("2006-01-02") != "2026-11-08" {
		t.Errorf("defaults: %+v", opts.query)
	}
}
