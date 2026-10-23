package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hedche/email-triage-bot/internal/config"
	"github.com/hedche/email-triage-bot/internal/decisionlog"
	"github.com/hedche/email-triage-bot/internal/triage"
)

func TestDispatchVersionAndUsage(t *testing.T) {
	if code := dispatch([]string{"version"}); code != 0 {
		t.Errorf("version exit = %d, want 0", code)
	}
	if code := dispatch(nil); code != 2 {
		t.Errorf("no args exit = %d, want 2", code)
	}
	if code := dispatch([]string{"nope"}); code != 2 {
		t.Errorf("unknown command exit = %d, want 2", code)
	}
}

func TestReplayCommandNeedsItsFlags(t *testing.T) {
	if code := cmdReplay([]string{"--decisions", "x.jsonl"}); code != 1 {
		t.Errorf("exit = %d, want 1 when --rules is missing", code)
	}
}

func TestReplayCommandReadsALogAndPrintsAReport(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "decisions.jsonl")
	w, err := decisionlog.NewWriter(logPath)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	rec := decisionlog.Record{
		Decision: triage.Decision{
			RunID: "r1", Account: "a@example.com", MessageID: "m1",
			Tier: triage.TierNone, Verdict: triage.VerdictLeave, Action: triage.ActionNone,
		},
		Message: decisionlog.MessageSummary{
			ID: "m1", Account: "a@example.com",
			From:    triage.Address{Name: "GitHub", Email: "noreply@github.com"},
			Subject: "PR merged",
		},
	}
	if err := w.Append(rec); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rulesPath := filepath.Join(dir, "rules.yaml")
	if err := os.WriteFile(rulesPath, []byte("threshold: 0.8\nsenders:\n  - domain: github.com\n"), 0o600); err != nil {
		t.Fatalf("write rules: %v", err)
	}

	if code := cmdReplay([]string{"--decisions", logPath, "--rules", rulesPath}); code != 0 {
		t.Fatalf("replay exit = %d, want 0", code)
	}
}

func TestSelectAccounts(t *testing.T) {
	accounts := []config.Account{
		{Email: "a@example.com", RefreshToken: "t1", Provider: config.ProviderGmail},
		{Email: "b@example.com", RefreshToken: "t2", Provider: config.ProviderGmail},
	}
	all, err := selectAccounts(accounts, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("selectAccounts(all) = %v, %v", all, err)
	}
	one, err := selectAccounts(accounts, "B@Example.com")
	if err != nil || len(one) != 1 || one[0].Email != "b@example.com" {
		t.Fatalf("selectAccounts(one) = %v, %v", one, err)
	}
	if _, err := selectAccounts(accounts, "nobody@example.com"); err == nil {
		t.Error("want an error for an unknown account")
	}
}

func TestBuildJudgeNoneIsANilInterface(t *testing.T) {
	j, err := buildJudge(&config.Settings{JudgeProvider: config.JudgeNone})
	if err != nil {
		t.Fatalf("buildJudge: %v", err)
	}
	if j != nil {
		t.Errorf("judge = %v, want an untyped nil so the run disables the brand tier", j)
	}
}

func TestBuildNotifierFallsBackToNop(t *testing.T) {
	if n := buildNotifier(&config.Settings{}); n == nil {
		t.Fatal("notifier is nil")
	}
	n := buildNotifier(&config.Settings{TelegramBotToken: "t", TelegramChatID: "c"})
	if n == nil {
		t.Fatal("notifier is nil")
	}
}

func TestNewLoggerHonoursFormatAndLevel(t *testing.T) {
	var buf bytes.Buffer
	log := newLogger(&buf, "json", "debug")
	log.Debug("hello", "k", "v")
	var got map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &got); err != nil {
		t.Fatalf("not JSON: %v (%q)", err, buf.String())
	}
	if got["msg"] != "hello" || got["k"] != "v" {
		t.Errorf("record = %v", got)
	}

	buf.Reset()
	log = newLogger(&buf, "text", "warn")
	log.Info("suppressed")
	log.Warn("shown")
	out := buf.String()
	if strings.Contains(out, "suppressed") || !strings.Contains(out, "shown") {
		t.Errorf("text output = %q", out)
	}
	if parseLevel("nonsense") != slog.LevelInfo {
		t.Error("unknown level should fall back to info")
	}
}
