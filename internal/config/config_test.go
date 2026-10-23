package config

import (
	"strings"
	"testing"
	"time"
)

// env makes a lookup over a fixed map, so tests never touch the real environment.
func env(m map[string]string) func(string) string {
	return func(key string) string { return m[key] }
}

// valid is the smallest environment that loads.
func valid(extra map[string]string) map[string]string {
	m := map[string]string{"GEMINI_API_KEY": "key-123"}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestLoadDefaults(t *testing.T) {
	s, err := LoadFrom(env(valid(nil)))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	tests := []struct {
		name string
		got  any
		want any
	}{
		{"RulesFile", s.RulesFile, "/config/rules.yaml"},
		{"AccountsFile", s.AccountsFile, "/config/accounts.yaml"},
		{"DryRun", s.DryRun, true},
		{"LookbackDays", s.LookbackDays, 7},
		{"MaxMessagesPerAccount", s.MaxMessagesPerAccount, 200},
		{"BodyMaxChars", s.BodyMaxChars, 2000},
		{"LabelRead", s.LabelRead, "Triage/Read"},
		{"LabelLeft", s.LabelLeft, "Triage/Left"},
		{"JudgeProvider", s.JudgeProvider, JudgeGemini},
		{"JudgeBatchSize", s.JudgeBatchSize, 10},
		{"JudgeMaxCallsPerRun", s.JudgeMaxCallsPerRun, 100},
		{"JudgeTimeout", s.JudgeTimeout, 60 * time.Second},
		{"GeminiModel", s.GeminiModel, "gemini-3.5-flash-lite"},
		{"LogFormat", s.LogFormat, "json"},
		{"LogLevel", s.LogLevel, "info"},
		{"DecisionsOut", s.DecisionsOut, ""},
		{"HealthcheckPingURL", s.HealthcheckPingURL, ""},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.want)
		}
	}
}

func TestLoadOverrides(t *testing.T) {
	s, err := LoadFrom(env(map[string]string{
		"RULES_FILE":               "/etc/rules.yaml",
		"ACCOUNTS_FILE":            "/etc/accounts.yaml",
		"DRY_RUN":                  "false",
		"LOOKBACK_DAYS":            "14",
		"MAX_MESSAGES_PER_ACCOUNT": "50",
		"BODY_MAX_CHARS":           "500",
		"LABEL_READ":               "Bot/Read",
		"LABEL_LEFT":               "Bot/Left",
		"JUDGE_PROVIDER":           "OpenAI",
		"JUDGE_BATCH_SIZE":         "5",
		"JUDGE_MAX_CALLS_PER_RUN":  "20",
		"JUDGE_TIMEOUT":            "90s",
		"OPENAI_BASE_URL":          "http://ollama:11434/v1",
		"OPENAI_API_KEY":           "sk-local",
		"OPENAI_MODEL":             "llama3.1",
		"GMAIL_CLIENT_ID":          "client-id",
		"GMAIL_CLIENT_SECRET":      "client-secret",
		"TELEGRAM_BOT_TOKEN":       "bot-token",
		"TELEGRAM_CHAT_ID":         "chat-id",
		"HEALTHCHECK_PING_URL":     "https://hc.example/ping",
		"DECISIONS_OUT":            "/tmp/decisions.jsonl",
		"LOG_FORMAT":               "TEXT",
		"LOG_LEVEL":                "DEBUG",
	}))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	tests := []struct {
		name string
		got  any
		want any
	}{
		{"RulesFile", s.RulesFile, "/etc/rules.yaml"},
		{"AccountsFile", s.AccountsFile, "/etc/accounts.yaml"},
		{"DryRun", s.DryRun, false},
		{"LookbackDays", s.LookbackDays, 14},
		{"MaxMessagesPerAccount", s.MaxMessagesPerAccount, 50},
		{"BodyMaxChars", s.BodyMaxChars, 500},
		{"LabelRead", s.LabelRead, "Bot/Read"},
		{"LabelLeft", s.LabelLeft, "Bot/Left"},
		{"JudgeProvider", s.JudgeProvider, JudgeOpenAI}, // lower-cased
		{"JudgeBatchSize", s.JudgeBatchSize, 5},
		{"JudgeMaxCallsPerRun", s.JudgeMaxCallsPerRun, 20},
		{"JudgeTimeout", s.JudgeTimeout, 90 * time.Second},
		{"OpenAIBaseURL", s.OpenAIBaseURL, "http://ollama:11434/v1"},
		{"OpenAIAPIKey", s.OpenAIAPIKey, "sk-local"},
		{"OpenAIModel", s.OpenAIModel, "llama3.1"},
		{"GmailClientID", s.GmailClientID, "client-id"},
		{"GmailClientSecret", s.GmailClientSecret, "client-secret"},
		{"TelegramBotToken", s.TelegramBotToken, "bot-token"},
		{"TelegramChatID", s.TelegramChatID, "chat-id"},
		{"HealthcheckPingURL", s.HealthcheckPingURL, "https://hc.example/ping"},
		{"DecisionsOut", s.DecisionsOut, "/tmp/decisions.jsonl"},
		{"LogFormat", s.LogFormat, "text"},
		{"LogLevel", s.LogLevel, "debug"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.want)
		}
	}
}

func TestLoadDryRun(t *testing.T) {
	tests := []struct {
		value   string
		want    bool
		wantErr bool
	}{
		{value: "true", want: true},
		{value: "TRUE", want: true},
		{value: "1", want: true},
		{value: "false", want: false},
		{value: "False", want: false},
		{value: "0", want: false},
		{value: " true ", want: true},
		{value: "yes", wantErr: true},
		{value: "maybe", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			s, err := LoadFrom(env(valid(map[string]string{"DRY_RUN": tt.value})))
			if tt.wantErr {
				requireErr(t, err, "DRY_RUN")
				return
			}
			if err != nil {
				t.Fatalf("LoadFrom: %v", err)
			}
			if s.DryRun != tt.want {
				t.Errorf("DryRun = %v, want %v", s.DryRun, tt.want)
			}
		})
	}
}

func TestLoadValidation(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{
			name:    "unknown judge provider",
			env:     map[string]string{"JUDGE_PROVIDER": "claude"},
			wantErr: `JUDGE_PROVIDER "claude"`,
		},
		{
			name:    "gemini without a key",
			env:     map[string]string{"JUDGE_PROVIDER": "gemini"},
			wantErr: "GEMINI_API_KEY is required",
		},
		{
			name:    "openai without a base url",
			env:     map[string]string{"JUDGE_PROVIDER": "openai", "OPENAI_MODEL": "llama3.1"},
			wantErr: "OPENAI_BASE_URL is required",
		},
		{
			name:    "openai without a model",
			env:     map[string]string{"JUDGE_PROVIDER": "openai", "OPENAI_BASE_URL": "http://x/v1"},
			wantErr: "OPENAI_MODEL is required",
		},
		{
			name:    "bad integer",
			env:     valid(map[string]string{"LOOKBACK_DAYS": "seven"}),
			wantErr: "LOOKBACK_DAYS",
		},
		{
			name:    "zero integer",
			env:     valid(map[string]string{"JUDGE_BATCH_SIZE": "0"}),
			wantErr: "JUDGE_BATCH_SIZE",
		},
		{
			name:    "negative integer",
			env:     valid(map[string]string{"MAX_MESSAGES_PER_ACCOUNT": "-1"}),
			wantErr: "MAX_MESSAGES_PER_ACCOUNT",
		},
		{
			name:    "bad duration",
			env:     valid(map[string]string{"JUDGE_TIMEOUT": "60"}),
			wantErr: "JUDGE_TIMEOUT",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadFrom(env(tt.env))
			requireErr(t, err, tt.wantErr)
		})
	}
}

func TestLoadJudgeNoneNeedsNothing(t *testing.T) {
	s, err := LoadFrom(env(map[string]string{"JUDGE_PROVIDER": "none"}))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if s.JudgeProvider != JudgeNone {
		t.Errorf("JudgeProvider = %q, want %q", s.JudgeProvider, JudgeNone)
	}
}

func TestLoadReadsTheProcessEnvironment(t *testing.T) {
	t.Setenv("JUDGE_PROVIDER", "gemini")
	t.Setenv("GEMINI_API_KEY", "key-from-env")
	t.Setenv("LOOKBACK_DAYS", "3")
	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.GeminiAPIKey != "key-from-env" || s.LookbackDays != 3 {
		t.Errorf("Load did not read the environment: %+v", s)
	}
}

func TestLoadFromNilLookupFallsBackToEnv(t *testing.T) {
	t.Setenv("JUDGE_PROVIDER", "gemini")
	t.Setenv("GEMINI_API_KEY", "key-from-env")
	if _, err := LoadFrom(nil); err != nil {
		t.Fatalf("LoadFrom(nil): %v", err)
	}
}

func requireErr(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want error containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err.Error(), want)
	}
}
