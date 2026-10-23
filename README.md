# Email Triage Bot

A scheduled bot that clears routine, no-action email from the unread queue of
one or more mailboxes, so that a morning inbox contains only mail a person
needs to look at. It runs once a day, scans unread mail from the last week,
decides each message with deterministic rules first and an AI judgement only
for what the rules cannot settle, and marks read or leaves accordingly. Every
message it judges gets a label, so its work is reviewable and reversible in
the mail client.

Rules are evaluated in four tiers, first match wins:

1. **Protect** — a sender, domain or subject keyword the bot must never touch.
2. **Sender** — an address or domain whose mail is always marked read.
3. **Keyword** — a case-insensitive subject substring whose mail is always marked read.
4. **Brand** — a company or product. A match here makes the message a
   *Candidate*: it is sent to the Judge, which returns a confidence and a
   one-sentence reason.

Anything matching no tier is left alone and is never sent to the Judge.

This repository is public. It contains no rules, no accounts, no tokens and no
mail. See [Privacy](#privacy).

## How a decision is made

For each account, the bot lists unread inbox messages from the last
`LOOKBACK_DAYS`, newest first, excluding anything that already carries an Audit
Label. It fetches headers, the snippet and a stripped, capped body for each,
then evaluates the tiers above.

A Protect match ends there with the verdict `leave` and nothing applied. A
Sender or Keyword match ends there with `mark-read`. A Brand match produces a
Candidate, which is batched (`JUDGE_BATCH_SIZE`) and sent to the Judge along
with the brand's note and examples. The Judge returns a confidence from 0 to 1
and a reason per candidate; at or above `threshold` the verdict is `mark-read`,
below it `leave`.

Marking read clears the unread flag and applies `Triage/Read`. Leaving applies
`Triage/Left` and changes nothing else. Both labels exist so the next run skips
the message, and so a person can review a morning's work with two label
searches.

If the Judge errors, times out, is over quota, or returns something that fails
validation, every affected candidate is left and the run is reported as failed.
There is no fallback judge and no default of marking read
([ADR 0003](docs/adr/0003-fail-closed-when-the-judge-is-unavailable.md)).
Candidates left this way get no label, so the next run judges them again.

The bot keeps no database and mounts no volume; the labels are the only state
([ADR 0001](docs/adr/0001-stateless-with-audit-labels.md)).

## Privacy

What leaves your network depends entirely on the Judge, and only for
Candidates — messages that matched a Brand rule:

| `JUDGE_PROVIDER` | What is sent, and where |
|---|---|
| `none` | Nothing. The Brand tier is disabled; deterministic rules only. |
| `openai` with a local endpoint (Ollama, llama.cpp) | Nothing leaves the LAN. |
| `openai` with a hosted endpoint (Groq and similar) | Sender name and address, subject, and up to `BODY_MAX_CHARS` of stripped body, to that provider. |
| `gemini` | The same, to Google. |

Messages that match Protect, Sender or Keyword rules, and messages that match
nothing, are never sent anywhere.

**Gemini must be billing-linked.** On the unpaid Gemini API tier, Google's
terms state that submitted content is used to improve its models and may be
read by human reviewers. Linking a billing account (Tier 1) moves the project
onto the paid terms, under which prompts are not used for training. At
Flash-Lite prices a household mailbox costs pennies a month. The free tier is
not supported for real mail
([ADR 0002](docs/adr/0002-paid-gemini-tier-for-privacy.md)).

**Rules, accounts and feedback never live in git.** The accounts file holds
Gmail refresh tokens. The rules file holds protected addresses and the brands
you buy from. Both are Secret entries in a secrets file kept outside every
repository ([ADR 0004](docs/adr/0004-preferences-deployed-via-secrets-pipeline.md)).
Decision Logs and tuning feedback are mail metadata and belong in a scratch
directory, never in a working tree. `.gitignore` and the gitleaks pre-commit
hook enforce this; enable the hook once per clone:

```bash
git config core.hooksPath .githooks    # or: make hooks
brew install gitleaks
```

## Quick start (local)

```bash
go build -o dist/email-triage-bot ./cmd/email-triage-bot
cp .env.example .env.local
```

1. **Create an OAuth desktop client.** In a Google Cloud project, enable the
   Gmail API, create an OAuth client of type *Desktop app*, and put its id and
   secret in `.env.local` as `GMAIL_CLIENT_ID` / `GMAIL_CLIENT_SECRET`. Add the
   `https://www.googleapis.com/auth/gmail.modify` scope, and read the
   [Gmail setup](#gmail-setup) section below before you leave the consent screen.
2. **Authorise each mailbox.**

   ```bash
   set -a; . ./.env.local; set +a
   ./dist/email-triage-bot auth --email someone@example.com
   ```

   This opens a browser and prints a refresh token.
3. **Write the two config files**, outside the repo, from
   [`examples/accounts.example.yaml`](examples/accounts.example.yaml) and
   [`examples/rules.example.yaml`](examples/rules.example.yaml). Point
   `ACCOUNTS_FILE` and `RULES_FILE` at them. Start with a small rules file:
   a few protect entries and two or three senders you are certain about.
4. **Dry run.**

   ```bash
   make run-dry     # DRY_RUN=true, writes dist/decisions.jsonl
   ```

5. **Read the log.** One line per scanned message, plus a run summary. Check
   the `mark-read` verdicts first — those are the ones that cost you something
   if they are wrong. When you are happy, run with `--dry-run=false`.

Before the first real run, consider the blast radius: everything the bot marks
read is also labelled `Triage/Read`, so `label:Triage/Read` in Gmail shows
exactly what it did, and selecting all and marking unread undoes it.

## Configuration

Everything is set by environment variable; the authoritative table, with
defaults and semantics, is the **[environment settings table in
docs/design.md](docs/design.md#environment-settings-internalconfigsettings)**,
and every variable is listed with comments in
[`.env.example`](.env.example). The ones worth knowing before you start:

- `DRY_RUN` — defaults to `true` everywhere, including in the container.
  Nothing changes in a mailbox until you set it to `false`.
- `RULES_FILE`, `ACCOUNTS_FILE` — default to `/config/rules.yaml` and
  `/config/accounts.yaml`, where the cluster mounts the two Secrets.
- `JUDGE_PROVIDER` — `gemini`, `openai`, or `none` to disable the Brand tier.
- `BODY_MAX_CHARS` — how much of a message the Judge sees. Lower it if that
  matters more to you than judgement quality.
- `JUDGE_MAX_CALLS_PER_RUN` — a quota ceiling. Candidates past it fail closed.

The rules and accounts file shapes are specified in
[docs/design.md](docs/design.md#files) and mirrored, with commentary, in
`examples/`.

## Gmail setup

- **Client type: Desktop app.** The `auth` command runs a loopback consent
  flow; a Web client will reject the redirect.
- **Scope: `gmail.modify`.** The bot needs to change the unread flag and apply
  labels. `gmail.readonly` is not enough, and the broader `https://mail.google.com/`
  is more than it needs.
- **Set the consent screen to "In production" before you generate refresh
  tokens.** This is the gotcha that will otherwise waste a week of your time:
  while the app is in *Testing*, Google expires refresh tokens after seven
  days, and the bot starts failing every Monday. Publishing does **not**
  require Google verification for this use — a single-operator app under 100
  users falls within the personal-use exemption, and an unverified app in
  production shows an "unverified app" interstitial once, at consent, and then
  issues durable tokens.
- **One client, many mailboxes.** `GMAIL_CLIENT_ID`/`GMAIL_CLIENT_SECRET` are
  shared; run `auth --email` once per account and collect the refresh tokens.

### Label behaviour

The bot creates `Triage/Read` and `Triage/Left` (configurable via `LABEL_READ`
and `LABEL_LEFT`) on first use, and applies exactly one of them to every
message it judges. They serve two purposes: they are your audit trail, and they
are how the bot avoids re-judging mail it has already seen — the unread query
excludes them client-side.

Clearing an Audit Label resets the bot for that message: it becomes eligible
again at the next run. That is the intended way to make it reconsider a
mailbox, and the intended way to undo a bad day — search `label:Triage/Read`,
mark unread, remove the label.

## Judges

`JUDGE_PROVIDER=gemini` uses the Gemini API directly with `GEMINI_API_KEY` and
`GEMINI_MODEL` (default `gemini-3.5-flash-lite`). Use a key from a
billing-linked project; see [Privacy](#privacy).

`JUDGE_PROVIDER=openai` speaks OpenAI-compatible chat completions to whatever
`OPENAI_BASE_URL` points at:

```bash
# Local Ollama — nothing leaves the LAN
JUDGE_PROVIDER=openai
OPENAI_BASE_URL=http://ollama:11434/v1
OPENAI_MODEL=llama3.1:8b
OPENAI_API_KEY=            # unused by Ollama, leave empty

# Hosted Groq
JUDGE_PROVIDER=openai
OPENAI_BASE_URL=https://api.groq.com/openai/v1
OPENAI_MODEL=llama-3.3-70b-versatile
OPENAI_API_KEY=REPLACE-WITH-YOUR-GROQ-KEY
```

Both implementations are given the same prompt and must return one judgement
per candidate; anything missing, malformed or out of range fails that candidate
closed. A small local model is usually fine for this task — it is a
narrow classification with a note and a couple of examples to anchor it — but
it is worth a dry run comparison before you trust it.

`JUDGE_PROVIDER=none` turns the Brand tier off entirely. The bot still runs
every deterministic rule, and nothing leaves the network.

## Deployment

The cluster side lives in the [Nature](https://github.com/hedche/Nature) GitOps
repository, not here:

- `kubernetes/email-triage-bot/` — namespace, CronJob (05:00 daily), and Flux
  image automation. The `ImagePolicy` sorts on the trailing timestamp of the
  `main-<sha>-<ts>` tags this repo's `docker-publish.yml` produces; changing
  the tag scheme breaks the rollout.
- Three Secret entries in the Nature secrets file, pushed with
  `scripts/secrets.sh push`:
  - `email-triage-rules` — the `rules.yaml` block scalar, mounted at `/config/rules.yaml`
  - `email-triage-accounts` — the `accounts.yaml` block scalar, mounted at `/config/accounts.yaml`
  - `email-triage-secrets` — API keys, the OAuth client, Telegram, healthcheck URL
- `DRY_RUN` is `true` in the CronJob's env until you have read a few days of
  logs. Flipping it to `false` is a one-line change and a Flux reconcile.
- The namespace must be added to the Alloy namespace allow-list in
  `kubernetes/logging/helmrelease.yaml`, or the decision logs never reach
  Grafana Cloud Loki — the allow-list is a `keep` rule, so an unlisted
  namespace is silently dropped.
- `EmailTriageSilent` in `kubernetes/monitoring-rules/rules/` is the alert that
  matters. A daily CronJob that stops running is invisible — no error, no page,
  just an inbox that quietly fills up again. The alert fires when a successful
  run has not been seen in over a day.

Images are published to `hedche/email-triage-bot` on Docker Hub by
`.github/workflows/docker-publish.yml` (multi-arch, amd64 + arm64). Secrets are
never committed as plaintext Kubernetes manifests.

## Tuning

The `triage-tune` Claude Code skill in `.claude/skills/triage-tune/` runs a
review session: it produces a Decision Log from a dry run, groups the decisions
by sender, walks you through them a few groups at a time, and turns your
disagreements into rule edits. It shows you a diff, replays the Decision Log
against the new rules so you can see exactly which verdicts change, and only
then writes the rules back into the secrets entry and pushes.

```
/triage-tune                       # offers to run a fresh dry run
/triage-tune dist/decisions.jsonl  # review a log you already have
```

The feedback it collects, and the Decision Log itself, stay in the session
scratch directory. Nothing personal is written into this repository.

## Telemetry

One structured log record per scanned message, at INFO:

```json
{"time":"...","level":"INFO","msg":"decision","run_id":"...","account":"someone@example.com",
 "message_id":"...","tier":"brand","brand":"Example Retailer","confidence":0.91,
 "reason":"Routine dispatch notification for an order","verdict":"mark-read",
 "action":"marked-read","dry_run":false,"judge":"gemini/gemini-3.5-flash-lite",
 "from":"noreply@example-retailer.com","subject":"Your order has been dispatched"}
```

Subjects are truncated to 80 characters. Message bodies are never logged.

One `run summary` record closes the run, with per-account and total `scanned`,
`marked_read`, `left`, `unmatched`, `judge_calls`, `judge_errors`,
`duration_ms`, `dry_run` and `failed_accounts`.

Exit code 0 on a clean run; **exit code 1** if any account failed or any Judge
call failed — which is what turns a bad run into a failed Job and a Telegram
message rather than a quiet morning. With `TELEGRAM_BOT_TOKEN` and
`TELEGRAM_CHAT_ID` set, the summary is also sent as a message;
`HEALTHCHECK_PING_URL` is pinged only after a fully successful run.

## Roadmap

**Phase 2: expiring mail.** A large share of what is left unread is mail with a
short shelf life — parcel delivery notifications, one-time passcodes, calendar
invitations for meetings that have happened. These want a different treatment
from routine mail: not "is this worth reading" but "is this still true". That
needs memory across runs, which
[ADR 0001](docs/adr/0001-stateless-with-audit-labels.md) deliberately does not
provide, so it will need that decision revisited.

Possible later work:

- More Providers: IMAP (which would cover most non-Gmail mail at once), and
  Outlook/Graph.
- Learned sender preferences — deriving sender and keyword rules from
  accumulated feedback rather than having a person write each one, with the
  same caveat about state.

## Prior art

Server-side mail filters (Sieve, Gmail filters) have done tier 1 to 3 for
decades and do them better than this; the reason to run anything at all is tier
4, where "is this routine mail from a brand I buy from" is not expressible as a
pattern. Tools like SaneBox and Gmail's own Priority Inbox answer that question
with a hosted model trained on your mail, which is a different trade to the one
made here: this bot keeps the rules legible and in your hands, sends as little
as it can get away with to a model you choose, and does one narrowly scoped
thing — clearing the unread flag — rather than sorting your mail for you.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for adding a Provider or a Judge, the
testing expectations, and the vocabulary discipline this codebase keeps
([CONTEXT.md](CONTEXT.md) is the glossary, and it is enforced in review).

## Licence

MIT. See [LICENSE](LICENSE).
