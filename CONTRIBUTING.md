# Contributing

Small repo, few moving parts, strong opinions about vocabulary and secrets.
Read [CONTEXT.md](CONTEXT.md) before writing anything — it is the glossary the
code, the logs and the docs all use, and reviews do enforce it.
[docs/design.md](docs/design.md) owns the shapes (env table, file formats, CLI,
telemetry, constructor signatures); [docs/adr/](docs/adr/) owns the why.

```bash
make hooks        # enable the gitleaks pre-commit hook (once per clone)
make lint vet test
```

## Adding a Provider

A Provider is one authorised mailbox on one email service. Gmail is the only
implementation today; IMAP and Outlook/Graph are the obvious next ones.

1. Create `internal/mail/<name>/` and implement `mail.Provider`
   (`internal/mail/provider.go`): `Account`, `ListUnread`, `Fetch`,
   `EnsureLabels`, `MarkRead`, `Label`.
2. Register it in `cmd/email-triage-bot` where accounts are turned into
   Providers, keyed on the `provider` field of the accounts file (absent means
   `gmail`). Nothing else in the codebase should switch on a provider name.

Semantics the run relies on, which your implementation must honour:

- **Newest first.** `ListUnread` returns ids in reverse chronological order.
  `Query.Max` truncates the tail, so a wrong order silently drops the mail that
  matters most.
- **Client-side exclusion of the Audit Labels.** Filter `Query.ExcludeLabels`
  yourself, in code, after the service query. Provider search syntax for nested
  labels (`Triage/Read`) is unreliable across services and across clients, and
  a silently-ignored exclusion means every message gets re-judged every day.
- **Batch the mutations.** `MarkRead` and `Label` take slices and must use the
  service's batch endpoint where one exists. They must also be idempotent:
  applying a label twice, or clearing an already-clear unread flag, is not an
  error.
- **Stripped, capped bodies.** `Fetch` returns a `triage.Message` whose `Body`
  has already been through `textclean.Strip` and capped to `bodyMax`. The
  engine and the Judge never see raw HTML, and `BODY_MAX_CHARS` is a privacy
  control, not a performance one — do not exceed it.
- **Single goroutine.** The run drives providers sequentially; you do not need
  to be concurrency-safe, and should not add concurrency internally without a
  reason.

Labels are the bot's only state ([ADR 0001](docs/adr/0001-stateless-with-audit-labels.md)).
If a service has no label-like concept, say so in an ADR before writing code —
it is a design change, not an implementation detail.

## Adding a Judge

A Judge decides whether a Candidate is routine mail from its Brand.

1. Create `internal/judge/<name>/` and implement `judge.Judge`
   (`internal/judge/judge.go`): `Name()` returning `provider/model` for
   telemetry, and `Judge(ctx, batch)`.
2. Use the shared `judge.BuildPrompt` and `judge.ParseJudgements` rather than
   writing your own prompt or parser. Every Judge asking the same question the
   same way is what makes a dry run against Ollama comparable to one against
   Gemini, and what lets a rule note written for one work with the other.
3. Register it in `cmd/email-triage-bot` alongside the existing
   `JUDGE_PROVIDER` values.

**Fail closed** ([ADR 0003](docs/adr/0003-fail-closed-when-the-judge-is-unavailable.md)).
Return an error for the whole batch on a transport error, a timeout or a quota
rejection; return a per-candidate result only when you actually have one. Never
invent a confidence, never retry into a different model, never default a
missing candidate to anything. The run turns every unanswered candidate into
the verdict `leave` and fails the run. A wrongly unread message costs a glance;
a wrongly read one can hide something that mattered.

Document, in the package comment and in the README's privacy table, exactly
what your implementation sends and to whom.

## Testing

- **No test touches the network.** Providers and Judges are tested against
  `httptest.Server` fakes serving recorded-shape responses. A test that needs
  credentials is a test that will not run in CI.
- Fake the awkward cases explicitly: malformed JSON, a judgement for a message
  id that was not in the batch, a confidence of `1.5`, an HTTP 429, a context
  deadline. Fail-closed behaviour is the thing most worth a test.
- The rules engine is pure and table-driven; add cases there rather than
  reaching for an end-to-end test.
- Test data uses `example.com` and invented brands. Never a real address, never
  a real subject line, never a pasted message body — the same rule as for rule
  Examples.
- `make lint vet test` is what CI runs (`.github/workflows/ci.yml`), plus a
  cross-compile for linux/amd64 and linux/arm64.

## Vocabulary

[CONTEXT.md](CONTEXT.md) defines the domain words — Account, Provider, Message,
Rule, Tier, Candidate, Judge, Confidence, Threshold, Decision, Verdict, Audit
Label, Triage Run, Dry Run, Fail Closed, Decision Log, Review, Feedback,
Example, Replay — and lists, for each, the synonyms not to use. Use the word
from the glossary in type names, field names, log keys, flags and prose. If you
need a concept the glossary does not have, add it there in the same format, in
the same commit.

## ADRs

Anything that changes what leaves the network, what state the bot keeps, how it
behaves when something is unavailable, or where configuration lives, gets an
ADR in `docs/adr/` before the code. Follow the existing format: frontmatter
with `status`, a title as a statement of the decision, one to three sentences of
body, then **Considered options** (with why each was rejected) and
**Consequences**. Number sequentially. Existing ADRs are not edited when
superseded; a new one supersedes them and says so.

## Secrets

- Nothing real in this repository. No refresh tokens, no API keys, no rules
  file, no accounts file, no Decision Log, no mail. `.gitignore` and
  `.gitleaks.toml` back this up; `make hooks` turns the pre-commit scan on.
- Live configuration lives in the Nature secrets file, outside every
  repository, and is pushed to the cluster with its `scripts/secrets.sh`
  ([ADR 0004](docs/adr/0004-preferences-deployed-via-secrets-pipeline.md)).
  Resolve its location by sourcing `scripts/secrets-path.sh`; never hardcode a
  path to it.
- Decision Logs and Feedback go in a scratch directory outside the working
  tree. They are mail metadata: sender addresses, subjects, and what a person
  thinks about them.
- If a secret is ever committed and pushed, rotate it. Rewriting history is
  cleanup; rotation is the fix.

## Commits

Conventional-commit prefixes (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`,
`chore:`), imperative subject under about 72 characters, lower case after the
prefix. The body explains why, not what — the diff covers what. Reference the
ADR when a commit implements one. Keep a commit to one concern; a rules-engine
change and a README rewrite are two commits.
