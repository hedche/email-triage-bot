---
name: triage-tune
description: Review Email Triage Bot decisions with the user and tune the rules that live in the Nature secrets file, including folding in rules added by Email Commands
allowed-tools: Bash, Read, Write, Edit, AskUserQuestion, Glob, Grep
---

# Triage tune

Walk the user through a Review of a Decision Log, turn their disagreements into
Rule edits, prove the effect with a Replay, and only then write the rules back
into the `email-triage-rules` Secret entry and push it.

Rules come from two places
([ADR 0005](../../../docs/adr/0005-email-commands-write-a-bot-owned-overlay.md)):
the rules file in the secrets file, and the **Overlay** — Changes the owner made
by forwarding mail to their `+triage` address, kept by the bot in the
`email-triage-rules-overlay` Secret on cereal. Every step below uses both, and
step 8 offers to fold Overlay Changes into the rules file.

Vocabulary is fixed by CONTEXT.md: Decision, Verdict, Candidate, Judge,
Example, Replay, Tier, Threshold, Feedback, Overlay, Change, Email Command. Use those words with the user.

## Hard rules

- **Never print a refresh token or an API key.** `accounts.yaml` and
  `email-triage-secrets` are read only to export environment variables; never
  echo their values, never write them to a file the user can see.
- **Never write a personal address, subject or brand into a file inside a
  repository.** Rules, Decision Logs and Feedback go in the session scratchpad
  directory. The rules file's only home is the secrets file.
- **Never hardcode the secrets file path.** Source Nature's
  `scripts/secrets-path.sh`, which sets `$SECRETS_FILE`.
- **Examples never quote message text.** An Example describes a *kind* of
  message ("Dispatch confirmation for an order"), never a real subject or body.
- **Apply nothing without explicit approval** for that specific diff.

## 1. Set up

1. Resolve paths. Nature is the sibling directory of this repo; resolve it to
   an absolute path and confirm it exists:

   ```bash
   NATURE="$(cd ../Nature && pwd)"
   source "$NATURE/scripts/secrets-path.sh"   # sets $SECRETS_FILE
   ```

2. Set `SCRATCH` to the session scratchpad directory and `mkdir -p` it. Every
   file this skill writes goes there.
3. Check `yq` is available (`command -v yq`), and stop with an install hint if
   not.
4. Fetch the Overlay. The default kubeconfig is empty; use Nature's:

   ```bash
   export KUBECONFIG="$NATURE/talos/kubeconfig"
   kubectl -n email-triage-bot get secret email-triage-rules-overlay \
     -o jsonpath='{.data.overlay\.yaml}' | base64 -d > "$SCRATCH/overlay.yaml"
   yq '.changes | length' "$SCRATCH/overlay.yaml"
   ```

   If it has Changes, list them for the user, one line each
   (`id`, `account` or "all accounts", `op`, `value`/`brand`, `note`), so they
   know what the bot is already doing beyond the rules file. Ignore `pending`,
   `seen` and `telegram_offset`: they are the bot's bookkeeping, not rules.

## 2. Get a Decision Log

If the user gave a path to a Decision Log (JSONL from
`run --dry-run --decisions-out`), use it.

Otherwise offer to produce one, and on agreement:

1. Extract the current Rules and Accounts into the scratchpad:

   ```bash
   yq '.kubernetes.secrets[] | select(.name=="email-triage-rules") | .data["rules.yaml"]' \
     "$SECRETS_FILE" > "$SCRATCH/rules.yaml"
   yq '.kubernetes.secrets[] | select(.name=="email-triage-accounts") | .data["accounts.yaml"]' \
     "$SECRETS_FILE" > "$SCRATCH/accounts.yaml"
   ```

2. Export the environment from the `email-triage-secrets` entry (API keys, the
   OAuth client) without displaying it, and point `RULES_FILE` / `ACCOUNTS_FILE`
   at the scratchpad copies and `RULES_OVERLAY_FILE` at `$SCRATCH/overlay.yaml`,
   so the Dry Run decides exactly as the 05:00 run will.
3. Run a Dry Run:

   ```bash
   go run ./cmd/email-triage-bot run --dry-run --decisions-out "$SCRATCH/decisions.jsonl"
   ```

   A Dry Run changes nothing in any mailbox. It does consult the Judge, so it
   costs a little quota; say so before running it.

## 3. Prepare the Review

Load the Decision Log records (`{"decision": {...}, "message": {...}}` per
line) and:

1. Drop records with `tier == "none"` — nothing matched them, the bot did not
   act, and there is nothing to agree or disagree with.
2. Group by `message.from.email`. For Brand-tier decisions, group by
   `from.email` **and** `decision.brand`, so one sender's two brands are two
   groups.
3. Sort groups by record count, descending. The user's time is best spent on
   the senders that generate the most mail.
4. For each group collect: sender name and address, count, up to 3 example
   subjects, the Tier and Rule (or Brand), the Confidence range across the
   group, one sample Reason, and the Verdict.

## 4. Ask

Present groups with **AskUserQuestion, four groups per call** — one question
per group. Each question shows the sender, the count, up to three example
subjects, the Tier/Rule (or Brand), the Confidence range and a sample Reason,
and the Verdict the bot reached.

Offer options that make sense for *that* Verdict:

- Verdict `mark-read` → "Agree", "Disagree: should be left", "Protect this sender"
- Verdict `leave` → "Agree", "Disagree: should be marked read", "Protect this sender"

Never offer a flip to the Verdict the bot already reached. "Protect this
sender" is always available: it is the strongest answer, and means *never touch
this sender again, whatever else matches*.

Skip any sender already answered in `$SCRATCH/feedback.jsonl` during this
session. Work through the groups in count order; stop when the user says so —
the top ten groups usually cover most of the volume.

## 5. Record Feedback

Append one JSON line per answer to `$SCRATCH/feedback.jsonl` — never to a file
in a repository:

```json
{"sender":"noreply@example.com","brand":"Example Retailer","tier":"brand","verdict":"mark-read","count":14,"answer":"disagree-leave","subjects":["..."],"confidence_range":[0.81,0.93]}
```

Feedback is private and stays in the scratchpad.

## 6. Propose rule edits

Translate disagreements into the smallest Rule change that fixes them. A
Decision whose Rule note is "added by email" (or that matches an Overlay Change)
came from the Overlay: fix it by removing that Change in step 8, not by adding a
counter-rule to the rules file.

| What the user said | The edit |
|---|---|
| Wrongly marked read by a Sender or Keyword Rule | Remove or narrow that Rule (a `domain` becomes a specific `address`; a broad `subject` becomes a longer substring). If they chose "Protect this sender", add a `protect` entry instead — it outranks every other tier. |
| Wrongly left, for a sender whose mail is always routine | Add a Sender Rule. Use `address` by default; use `domain` only when every subject in the group looks machine-generated and the user confirms the whole domain is safe. |
| Brand misjudgement | Adjust that Brand Rule's `note` to name the distinction the Judge missed, and add an `Example` with `description` + `verdict` describing the *kind* of message — never quoting one. |
| Several errors clustered just either side of the Threshold | Propose a Threshold change, with the numbers: how many Decisions in this log move, and which. Do not touch the Threshold to fix a single sender — that is a Rule's job. |

Write the result to `$SCRATCH/rules.new.yaml` and show the user the diff:

```bash
diff -u "$SCRATCH/rules.yaml" "$SCRATCH/rules.new.yaml"
```

Keep the file's existing comments and ordering; add a short comment on each new
Rule saying what it came from ("added from review, 2026-09-16").

## 7. Verify with a Replay

```bash
go run ./cmd/email-triage-bot replay \
  --decisions "$SCRATCH/decisions.jsonl" --rules "$SCRATCH/rules.new.yaml" \
  --overlay "$SCRATCH/overlay.new.yaml"
```

`overlay.new.yaml` is `overlay.yaml` minus any Changes being folded or removed
in step 8 (copy it unchanged if there are none). Replaying against both is what
proves that folding a Change into the rules file changes no Verdict.

Replay is offline: it re-runs the deterministic Tiers and the Brand pre-filter
over the saved Messages, contacting neither the Provider nor the Judge. Show
the report and read it with the user:

- Verdict changes are the point — check each one is one they asked for.
- Messages reported as **"needs judge"** have become Candidates and have no
  Verdict yet. Replay cannot decide them. Flag these explicitly and say that
  confirming them needs a fresh Dry Run after the rules are applied.
- Unexpected changes to senders nobody discussed mean a Rule is too broad. Go
  back to step 6.

## 8. Fold the Overlay

Skip this if the Overlay has no Changes. Otherwise ask, with AskUserQuestion
(up to four Changes per call), what to do with each:

- **Fold into rules** (recommended for anything older than a week that the
  Review agreed with): add the equivalent Rule to `rules.new.yaml` with the
  comment "from email command <id>, <created_at date>", and drop the Change
  from `overlay.new.yaml`. The mapping is direct: `protect_*` → `protect`
  (`sender`/`domain`/`keyword`), `read_sender`/`read_domain` → `senders`
  (`address`/`domain`), `read_keyword` → `keywords`, `brand_note` → append to
  that Brand's `note`, `brand_example` → add to its `examples`. A Change with an
  `account` goes under `accounts.<account>`.
- **Keep in the Overlay**: leave it.
- **Remove**: drop it from `overlay.new.yaml` and add nothing.

Re-run the step 7 Replay after deciding. Folding must change no Verdict;
removing may, and each such change should be one the user asked for.

## 9. Apply

Only after the user has explicitly approved this diff:

```bash
yq -i '(.kubernetes.secrets[] | select(.name=="email-triage-rules") | .data["rules.yaml"]) = load_str("'"$SCRATCH"'/rules.new.yaml")' "$SECRETS_FILE"
cd "$NATURE" && ./scripts/secrets.sh push --cluster cereal email-triage-rules
```

If step 8 changed the Overlay, write it back **after** the rules push
succeeds, so a Change is never in neither place (in both is harmless: Changes
only add). Edit only `.changes` of the live Secret, so Proposals and Telegram
state the bot wrote since step 1 survive. `kubectl replace` carries the
resourceVersion it read, so it fails rather than overwrites if the bot's
15-minute Job saved in between; on a conflict, fetch and redo this step.

```bash
kubectl -n email-triage-bot get secret email-triage-rules-overlay -o json > "$SCRATCH/overlay-secret.json"
jq -r '.data["overlay.yaml"]' "$SCRATCH/overlay-secret.json" | base64 -d > "$SCRATCH/overlay.live.yaml"
yq -i '.changes = load("'"$SCRATCH"'/overlay.new.yaml").changes' "$SCRATCH/overlay.live.yaml"
jq --arg v "$(base64 < "$SCRATCH/overlay.live.yaml" | tr -d '\n')" '.data["overlay.yaml"] = $v' \
  "$SCRATCH/overlay-secret.json" | kubectl replace -f -
```

This is the one Secret it is right to change outside GitOps: Flux created it
once (`ssa: IfNotPresent`) and never applies it again; its content belongs to
the bot.

Then tell the user:

- The change is live at the **next 05:00 Triage Run**; nothing happens now.
- The rules are not in git ([ADR 0004](../../../docs/adr/0004-preferences-deployed-via-secrets-pipeline.md));
  the secrets file is the only copy. A bad edit is fixed by another edit.
- If `DRY_RUN` is currently `false`, suggest a Dry Run first —
  `run --dry-run --decisions-out <path>` with the new rules — before the bot
  acts on them for real.
- Anything the bot got wrong in the mailbox is still fixable: search
  `label:Triage/Read`, mark unread, clear the label.
