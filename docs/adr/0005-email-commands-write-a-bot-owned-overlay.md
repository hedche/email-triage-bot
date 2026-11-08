---
status: accepted
---

# Email Commands change rules through a bot-owned Overlay, verified by Gmail's SENT label

A mailbox owner can change their rules by forwarding a message to their own plus-address (`someone+triage@gmail.com`) with a note above it. The `listen` subcommand, running continuously in the cluster (`commands` is the one-shot form), reads these, asks Gemini to turn the note into Changes, and writes them to an Overlay: a second Secret, `email-triage-rules-overlay`, which the bot owns and which is laid over the rules file additively. Protective Changes apply at once; anything that could cause mail to be marked read waits for an Apply tap in Telegram.

This amends [ADR 0004](0004-preferences-deployed-via-secrets-pipeline.md): the rules file in the secrets file is still the only thing a person edits by hand, and nothing the bot does changes it.

## Why these choices

- **Origin is proved by the SENT label, not by headers.** A message carries Gmail's `SENT` label only if the account itself sent it; no outside sender can apply it. That is stronger than checking SPF, DKIM or DMARC on inbound mail, which forwarding routinely breaks and which an Email Routing worker cannot fully see. It also needs no new inbox, MX record or public endpoint: the bot already holds `gmail.modify` on every Account.
- **A separate Overlay, not edits to the rules Secret.** The rules Secret is pushed from the Mac by `secrets.sh`; a bot write would be overwritten on the next push. The Overlay is created once by Flux with `kustomize.toolkit.fluxcd.io/ssa: IfNotPresent` and from then on belongs to the bot, which has `get` and `update` on that one Secret only. Changes only add Rules, so an email can never remove a Rule the rules file defines; it can remove only Changes it added earlier.
- **The model sees the owner's words, never the forwarded body.** The Interpreter is given the note, the forwarded message's sender and subject (fenced and labelled as third-party data), and the Changes made earlier. It has no tools and answers in a fixed JSON schema of eight operations. Code then grounds every Change: its address, domain, subject text or brand must come from the note or the forwarded sender and subject, or it is dropped. A forwarded phishing mail that says "mark everything from bank.example read" cannot produce that Rule.
- **Confirmation in the dangerous direction only.** Protect Rules and `leave` Examples can only make the bot leave more mail alone, so they apply immediately and are reported. Sender, Keyword and Brand-note Changes, and removal of a Protect Change, wait for Apply. Proposals expire after seven days.
- **Taps by long polling, not a webhook (amended 2026-11-08).** The first version read taps with `getUpdates` from the 15-minute Job, which left the button spinning for up to 15 minutes, because Telegram expects an answer within seconds. `listen` now runs as one always-on pod. It holds a long poll (`getUpdates`, 50 s), so a tap is answered within a second, and it reads the mailboxes every minute. A webhook would be just as quick but needs a public HTTPS endpoint, and cereal is tailnet-only by design. A bot can have only one consumer of its updates, so the commands use a bot of their own: the first one was shared with Peanut, and something else polling that token took the taps.

## Considered options

- **Cloudflare Email Routing to a Worker at a separate address.** Lets anyone email the bot, which is the problem: origin then rests on DMARC alignment of forwarded mail, and the cluster is not reachable from the Worker without a tunnel or queue.
- **Proposal only, applied by a person with `triage-tune`.** Safest, but turns a ten-second email into a laptop session, which is the friction this exists to remove.
- **Writing to the rules Secret.** Simplest code; loses every change on the next `secrets.sh push` unless something syncs back.
- **Replying YES by email instead of Telegram.** Works within Gmail alone, but Telegram is already the channel for the run summary and a button is harder to fat-finger than a reply.

## Consequences

- The bot now keeps state: the Overlay holds Changes, pending Proposals, the Telegram offset and the IDs of commands already handled. ADR 0001's "labels are the only state" still holds for Triage Runs; Email Commands are a separate concern with their own Secret.
- Rules now come from two places. `triage-tune` should read both, and fold Overlay Changes into the rules file from time to time (then delete them from the Overlay).
- The Interpreter uses the same paid Gemini project as the Judge (ADR 0002); it is sent the owner's note and the forwarded sender and subject, nothing else.
