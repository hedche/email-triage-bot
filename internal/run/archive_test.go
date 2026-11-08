package run

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hedche/email-triage-bot/internal/decisionlog"
	"github.com/hedche/email-triage-bot/internal/mail"
	"github.com/hedche/email-triage-bot/internal/triage"
)

type memArchive struct {
	objects map[string][]byte
	err     error
}

func (m *memArchive) Put(_ context.Context, key string, body []byte, _ string) error {
	if m.err != nil {
		return m.err
	}
	if m.objects == nil {
		m.objects = map[string][]byte{}
	}
	m.objects[key] = body
	return nil
}

func archiveRules() *triage.Rules {
	return &triage.Rules{Threshold: 0.8, RuleSet: triage.RuleSet{Senders: []triage.SenderRule{{Domain: "github.com"}}}}
}

func TestRunArchivesARecordPerMessage(t *testing.T) {
	store := &memArchive{}
	p := provider("a@example.com",
		msg("m1", "a@example.com", "GitHub", "noreply@github.com", "PR merged"),
		msg("m2", "a@example.com", "A Human", "human@elsewhere.test", "lunch?"),
	)
	if _, err, _ := runWith(t, Options{Settings: testSettings(), Rules: archiveRules(), Providers: []mail.Provider{p}, Archive: store}); err != nil {
		t.Fatal(err)
	}
	if len(store.objects) != 2 {
		t.Fatalf("objects = %d, want records and summary", len(store.objects))
	}
	for key, body := range store.objects {
		if !strings.HasPrefix(key, "runs/") {
			t.Errorf("key %q", key)
		}
		if !strings.HasSuffix(key, ".jsonl.gz") {
			continue
		}
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		recs, err := decisionlog.ReadFrom(zr)
		if err != nil {
			t.Fatal(err)
		}
		if len(recs) != 2 || recs[0].Message.Ref == "" {
			t.Errorf("records %+v", recs)
		}
	}
}

func TestArchiveFailureDoesNotFailTheRun(t *testing.T) {
	store := &memArchive{err: errors.New("bucket unavailable")}
	p := provider("a@example.com", msg("m1", "a@example.com", "GitHub", "noreply@github.com", "PR merged"))
	sum, err, _ := runWith(t, Options{Settings: testSettings(), Rules: archiveRules(), Providers: []mail.Provider{p}, Archive: store})
	if err != nil || sum.Failed() {
		t.Errorf("err %v, failed %v", err, sum.Failed())
	}
}
