// Package config turns the environment into Settings and reads the accounts
// file. Vocabulary follows CONTEXT.md: an Account is one mailbox the bot is
// authorised to act on, a Provider is the service it lives on.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Settings is every environment variable the bot reads, already typed and
// defaulted. See docs/design.md for the table this mirrors.
type Settings struct {
	RulesFile    string
	AccountsFile string

	DryRun                bool
	LookbackDays          int
	MaxMessagesPerAccount int
	BodyMaxChars          int

	LabelRead string
	LabelLeft string

	JudgeProvider       string
	JudgeBatchSize      int
	JudgeMaxCallsPerRun int
	JudgeTimeout        time.Duration

	GeminiAPIKey string
	GeminiModel  string

	OpenAIBaseURL string
	OpenAIAPIKey  string
	OpenAIModel   string

	GmailClientID     string
	GmailClientSecret string

	TelegramBotToken string
	TelegramChatID   string

	HealthcheckPingURL string
	DecisionsOut       string

	LogFormat string
	LogLevel  string
}

// Judge provider names for JUDGE_PROVIDER.
const (
	JudgeGemini = "gemini"
	JudgeOpenAI = "openai"
	JudgeNone   = "none"
)

// lookupEnv is the environment Load reads. Tests replace it (or use t.Setenv).
var lookupEnv func(string) string = os.Getenv

// Load reads Settings from the process environment, applying the documented
// defaults and validating the combinations that cannot work.
func Load() (*Settings, error) { return LoadFrom(lookupEnv) }

// LoadFrom reads Settings from an arbitrary lookup, for tests and callers that
// hold their own environment.
func LoadFrom(lookup func(string) string) (*Settings, error) {
	if lookup == nil {
		lookup = os.Getenv
	}
	get := func(key, def string) string {
		if v := strings.TrimSpace(lookup(key)); v != "" {
			return v
		}
		return def
	}

	s := &Settings{
		RulesFile:          get("RULES_FILE", "/config/rules.yaml"),
		AccountsFile:       get("ACCOUNTS_FILE", "/config/accounts.yaml"),
		LabelRead:          get("LABEL_READ", "Triage/Read"),
		LabelLeft:          get("LABEL_LEFT", "Triage/Left"),
		JudgeProvider:      strings.ToLower(get("JUDGE_PROVIDER", JudgeGemini)),
		GeminiAPIKey:       get("GEMINI_API_KEY", ""),
		GeminiModel:        get("GEMINI_MODEL", "gemini-3.5-flash-lite"),
		OpenAIBaseURL:      get("OPENAI_BASE_URL", ""),
		OpenAIAPIKey:       get("OPENAI_API_KEY", ""),
		OpenAIModel:        get("OPENAI_MODEL", ""),
		GmailClientID:      get("GMAIL_CLIENT_ID", ""),
		GmailClientSecret:  get("GMAIL_CLIENT_SECRET", ""),
		TelegramBotToken:   get("TELEGRAM_BOT_TOKEN", ""),
		TelegramChatID:     get("TELEGRAM_CHAT_ID", ""),
		HealthcheckPingURL: get("HEALTHCHECK_PING_URL", ""),
		DecisionsOut:       get("DECISIONS_OUT", ""),
		LogFormat:          strings.ToLower(get("LOG_FORMAT", "json")),
		LogLevel:           strings.ToLower(get("LOG_LEVEL", "info")),
	}

	var err error
	if s.DryRun, err = parseBool(get("DRY_RUN", "true"), "DRY_RUN"); err != nil {
		return nil, err
	}
	if s.LookbackDays, err = parsePositiveInt(get("LOOKBACK_DAYS", "7"), "LOOKBACK_DAYS"); err != nil {
		return nil, err
	}
	if s.MaxMessagesPerAccount, err = parsePositiveInt(get("MAX_MESSAGES_PER_ACCOUNT", "200"), "MAX_MESSAGES_PER_ACCOUNT"); err != nil {
		return nil, err
	}
	if s.BodyMaxChars, err = parsePositiveInt(get("BODY_MAX_CHARS", "2000"), "BODY_MAX_CHARS"); err != nil {
		return nil, err
	}
	if s.JudgeBatchSize, err = parsePositiveInt(get("JUDGE_BATCH_SIZE", "10"), "JUDGE_BATCH_SIZE"); err != nil {
		return nil, err
	}
	if s.JudgeMaxCallsPerRun, err = parsePositiveInt(get("JUDGE_MAX_CALLS_PER_RUN", "100"), "JUDGE_MAX_CALLS_PER_RUN"); err != nil {
		return nil, err
	}
	if s.JudgeTimeout, err = parseDuration(get("JUDGE_TIMEOUT", "60s"), "JUDGE_TIMEOUT"); err != nil {
		return nil, err
	}

	if err := s.validate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Settings) validate() error {
	switch s.JudgeProvider {
	case JudgeGemini:
		if s.GeminiAPIKey == "" {
			return fmt.Errorf("GEMINI_API_KEY is required when JUDGE_PROVIDER is %q", JudgeGemini)
		}
	case JudgeOpenAI:
		if s.OpenAIBaseURL == "" {
			return fmt.Errorf("OPENAI_BASE_URL is required when JUDGE_PROVIDER is %q", JudgeOpenAI)
		}
		if s.OpenAIModel == "" {
			return fmt.Errorf("OPENAI_MODEL is required when JUDGE_PROVIDER is %q", JudgeOpenAI)
		}
	case JudgeNone:
		// Brand tier disabled; nothing else required.
	default:
		return fmt.Errorf("JUDGE_PROVIDER %q: must be %q, %q or %q",
			s.JudgeProvider, JudgeGemini, JudgeOpenAI, JudgeNone)
	}
	return nil
}

func parseBool(v, key string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1":
		return true, nil
	case "false", "0":
		return false, nil
	default:
		return false, fmt.Errorf("%s %q: must be true, false, 1 or 0", key, v)
	}
}

func parsePositiveInt(v, key string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("%s %q: must be a whole number", key, v)
	}
	if n <= 0 {
		return 0, fmt.Errorf("%s %q: must be greater than zero", key, v)
	}
	return n, nil
}

func parseDuration(v, key string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("%s %q: must be a duration such as 60s", key, v)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s %q: must be greater than zero", key, v)
	}
	return d, nil
}
