# Design contract

This file is the build contract every package is written against. CONTEXT.md
owns the vocabulary; docs/adr/ owns the why. This owns the shapes.

## Module layout

```
cmd/email-triage-bot/      CLI: run | commands | auth | replay | version
internal/triage/           domain types (types.go) + rules engine (engine.go) + rules loading (rules.go)
internal/config/           Settings from env; accounts file loading
internal/textclean/        HTML/plain-text stripping and capping for message bodies
internal/decisionlog/      Decision Log JSONL writer/reader (one record per scanned message)
internal/mail/             Provider interface;  internal/mail/gmail/  Gmail implementation + OAuth helper
internal/judge/            Judge interface;     internal/judge/gemini/  internal/judge/openai/
internal/run/              the Triage Run orchestrator (per-account loop, batching, actions, summary)
internal/notify/           Telegram summary (optional)
internal/command/          Email Commands: plus-address commands -> Overlay Changes (ADR 0005)
examples/                  rules.example.yaml, accounts.example.yaml
.claude/skills/triage-tune Claude skill for reviewing decisions and tuning rules
```

## Environment settings (internal/config.Settings)

| Variable | Default | Notes |
|---|---|---|
| `RULES_FILE` | `/config/rules.yaml` | mounted from the `email-triage-rules` Secret |
| `RULES_OVERLAY_FILE` | `/config/overlay/overlay.yaml` | Changes made by Email Commands; a missing file is no Changes |
| `ACCOUNTS_FILE` | `/config/accounts.yaml` | mounted from the `email-triage-accounts` Secret |
| `DRY_RUN` | `true` | `run --dry-run=false` overrides |
| `LOOKBACK_DAYS` | `7` | |
| `MAX_MESSAGES_PER_ACCOUNT` | `200` | newest first |
| `BODY_MAX_CHARS` | `2000` | |
| `LABEL_READ` | `Triage/Read` | Audit Label for mark-read |
| `LABEL_LEFT` | `Triage/Left` | Audit Label for leave |
| `JUDGE_PROVIDER` | `gemini` | `gemini` or `openai`; `none` disables the Brand tier |
| `JUDGE_BATCH_SIZE` | `10` | candidates per Judge call |
| `JUDGE_MAX_CALLS_PER_RUN` | `100` | across all accounts; further candidates fail closed |
| `JUDGE_TIMEOUT` | `60s` | per call |
| `GEMINI_API_KEY` | | required when provider is gemini |
| `GEMINI_MODEL` | `gemini-3.5-flash-lite` | |
| `OPENAI_BASE_URL` | | e.g. `http://ollama:11434/v1`, `https://api.groq.com/openai/v1` |
| `OPENAI_API_KEY` | | optional for local endpoints |
| `OPENAI_MODEL` | | required when provider is openai |
| `GMAIL_CLIENT_ID` / `GMAIL_CLIENT_SECRET` | | OAuth desktop client shared by all accounts |
| `TELEGRAM_BOT_TOKEN` / `TELEGRAM_CHAT_ID` | | both set enables the per-run summary |
| `OVERLAY_SECRET` | | `commands` only: the Secret holding the Overlay, e.g. `email-triage-rules-overlay`; `commands --overlay PATH` uses a local file instead |
| `COMMAND_TAG` | `triage` | `commands` only: the plus-address tag, so commands go to `local+triage@domain` |
| `LABEL_COMMAND` | `Triage/Command` | applied to handled Email Commands, which are also marked read |
| `COMMANDS_TELEGRAM_BOT_TOKEN` | `TELEGRAM_BOT_TOKEN` | `commands` only; must be a bot with no webhook, because taps are read with getUpdates |
| `HEALTHCHECK_PING_URL` | | GET after a fully successful run; empty disables |
| `DECISIONS_OUT` | | path for the Decision Log; `run --decisions-out` overrides |
| `LOG_FORMAT` | `json` | `json` or `text` |
| `LOG_LEVEL` | `info` | |

## Files

### rules.yaml

```yaml
threshold: 0.8
protect:
  - sender: alice@example.com
  - domain: example-corp.com      # includes subdomains
  - keyword: invoice              # case-insensitive subject substring
senders:
  - address: noreply@github.com
  - domain: royalmail.com
    note: tracking updates
keywords:
  - subject: your weekly digest
brands:
  - name: Amazon
    note: Order, dispatch and delivery notifications are routine. Payment problems and account security mail are not.
    examples:
      - description: Dispatch confirmation for an order
        verdict: mark-read
      - description: Unusual sign-in attempt
        verdict: leave
accounts:                          # optional, additive per account
  someone@gmail.com:
    protect:
      - sender: boss@example.com
    brands:
      - name: Octopus Energy
```

Matching semantics: all comparisons are case-insensitive. `domain` matches the
domain and any subdomain. `sender`/`address` match the address only, never the
display name. `keyword`/`subject` are substring matches on the subject. A brand
matches when its name appears as a substring in the sender name, the sender
domain (with spaces removed from the brand name) or the subject.

### accounts.yaml

```yaml
accounts:
  - email: someone@gmail.com
    refresh_token: "1//..."
    provider: gmail        # optional, default gmail
```

### Decision Log (JSONL, one object per scanned message)

```json
{"decision": {...triage.Decision...}, "message": {"id","account","from":{"name","email"},"subject","snippet","received"}}
```

Body is never written to the Decision Log. `replay` needs only from + subject.

## Telemetry

One `slog` record per scanned message at INFO, `msg="decision"`, with the
Decision fields flattened plus `from`, `subject` truncated to 80 chars. One
`msg="run summary"` record at the end with per-account and total counts:
`scanned`, `marked_read`, `left`, `unmatched`, `judge_calls`, `judge_errors`,
`duration_ms`, `dry_run`, `failed_accounts`. Exit code 1 if any account failed
or any Judge call failed.

Before any Account is triaged the run lists each one's unread IDs and compares
them. A provider message ID is unique only within a mailbox, so one ID in
common means two Accounts are one mailbox reached through two credentials —
the shape a copied `refresh_token` takes. Both are skipped (`msg="account
skipped"`, at WARN, `shares_mailbox_with`), because nothing says which name
the credentials belong to. A skip is a warning, not a failure: it shows as ⏭️
in the summary but does not fail the run, since a failed run only made the
Job retry into the same skip, up to three times a day.

Two renderings of the same `run.Summary`, which must not be confused:
`Summary.Text()` is plain text for stderr and the pod logs;
`Summary.TelegramHTML()` is the notification, in Telegram's HTML parse mode.
HTML, not MarkdownV2, because it reserves three characters rather than
eighteen — addresses, brands and provider errors interpolate without an
escaping bug silently costing the whole message. The layout targets a phone:
no `<pre>` block (Telegram scrolls those sideways instead of wrapping), zero
counts dropped, per-account detail inside an expandable `<blockquote>`, and
failures rendered above that quote so nothing that needs attention is hidden
behind a tap.

A Dry Run also sends `Summary.ReportHTML()` as a file (`triage-<run_id>.html`,
Telegram `sendDocument`), after the summary. It lists every scanned message
per account under collapsible Would be read / Left / Unmatched sections: the
sender, the subject capped at 45 characters, and why it went there (the
matched rule, or the Judge's reason and confidence). It is a file because a
full lookback window is far past Telegram's 4096-character message limit.
Live runs neither collect the items nor send the file; once live, the Audit
Labels in Gmail are the review.

## CLI

```
email-triage-bot run     [--dry-run[=false]] [--account EMAIL] [--decisions-out PATH]
email-triage-bot auth    [--email EMAIL]           # local OAuth consent flow; prints refresh token
email-triage-bot replay  --decisions PATH --rules PATH [--account EMAIL]   # offline: no provider, no judge
email-triage-bot version
```

`replay` re-runs the deterministic tiers and the Brand pre-filter over the
saved messages and prints, per message whose Verdict or Tier would change,
the before and after. Messages that would now be Candidates are reported as
"needs judge" rather than given a Verdict.

## Constructor signatures (so cmd/ can be written before packages land)

```go
config.Load() (*config.Settings, error)                         // from env
config.LoadAccounts(path string) ([]config.Account, error)
triage.LoadRules(path string) (*triage.Rules, error)            // validates
(*triage.Rules).For(account string) triage.RuleSet              // global + per-account, additive
triage.NewEngine(rs triage.RuleSet, threshold float64) *triage.Engine
(*triage.Engine).Evaluate(m triage.Message) triage.Decision     // fills Tier/Rule/Brand/Verdict for deterministic tiers;
                                                                // for a Brand match returns Tier=brand, Verdict="" (needs judge)
(*triage.Engine).Candidate(m triage.Message) (triage.Candidate, bool)
textclean.Strip(plain, html string, max int) string             // prefer plain; strip html; collapse; cap
decisionlog.NewWriter(path string) (*decisionlog.Writer, error) // Append(rec Record) error; Close()
decisionlog.Read(path string) ([]decisionlog.Record, error)
gmail.New(ctx, clientID, clientSecret, refreshToken, account string) (*gmail.Provider, error)
gmail.Authorize(ctx, clientID, clientSecret string, out io.Writer) (refreshToken string, err error)
gemini.New(apiKey, model string, timeout time.Duration) (*gemini.Judge, error)
openai.New(baseURL, apiKey, model string, timeout time.Duration) (*openai.Judge, error)
run.New(opts run.Options) *run.Runner                          // Options{Settings, Rules, Providers []mail.Provider, Judge judge.Judge, Log *slog.Logger, Decisions *decisionlog.Writer, Notifier notify.Notifier}
(*run.Runner).Run(ctx) (run.Summary, error)
notify.NewTelegram(token, chatID string) notify.Notifier         // Notify(ctx, text string) error; text is Telegram HTML
                                                                  // also a notify.DocumentNotifier: NotifyDocument(ctx, filename, content, caption)
```
