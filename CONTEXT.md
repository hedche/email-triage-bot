# Email Triage

A scheduled bot that clears routine, no-action email from the unread queue of one or more mailboxes, using deterministic rules first and an AI judgement only for the remainder. It exists so that a morning inbox contains only mail a person needs to look at.

## Language

### Mailboxes

**Account**:
One mailbox the bot is authorised to act on. The bot processes every configured Account in one Triage Run.
_Avoid_: inbox, user, mailbox, profile

**Provider**:
The email service an Account lives on. Gmail is the only Provider today.
_Avoid_: backend, source, connector

**Message**:
One email. Every Message is judged and acted on individually, never as part of a thread.
_Avoid_: email, mail item, thread

### Rules

**Rule**:
A configured pattern that, when it matches a Message, determines its Decision without further evaluation. Every Rule belongs to exactly one Tier.
_Avoid_: filter, list entry, pattern

**Tier**:
The precedence class of a Rule. Tiers are evaluated in a fixed order: Protect, Sender, Keyword, Brand. The first Tier with a matching Rule wins.
_Avoid_: level, stage, priority

**Protect Rule**:
A Rule naming a sender, domain or subject keyword whose Messages the bot must never touch.
_Avoid_: whitelist, allowlist, exclusion, safe list

**Sender Rule**:
A Rule naming an exact address or a domain (including its subdomains) whose Messages are always marked read.
_Avoid_: address rule, blocklist, auto-read list

**Keyword Rule**:
A Rule naming a case-insensitive substring of the subject whose Messages are always marked read.
_Avoid_: subject rule, phrase

**Brand Rule**:
A Rule naming a company or product, with an optional description of the kind of mail that is routine from it. Messages that mention the brand become Candidates rather than being decided outright.
_Avoid_: company rule, AI rule, fuzzy rule

### Judgement

**Candidate**:
A Message that matched no Protect, Sender or Keyword Rule and mentions a Brand in its sender name, sender domain or subject. Only Candidates are sent to the Judge.
_Avoid_: unmatched message, AI input, leftover

**Judge**:
The component that decides whether a Candidate is routine mail from its Brand, returning a Confidence and a Reason. Gemini and any OpenAI-compatible endpoint are the Judge implementations.
_Avoid_: classifier, model, LLM, AI, scorer

**Confidence**:
The Judge's self-reported certainty, from 0 to 1, that a Candidate is routine mail from its Brand.
_Avoid_: probability, score, likelihood

**Reason**:
The Judge's one-sentence explanation of its Confidence, recorded with every Decision it contributes to.
_Avoid_: rationale, explanation, justification

**Threshold**:
The Confidence at or above which a Candidate's Verdict is mark-read. Below it the Verdict is leave.
_Avoid_: cutoff, minimum score

### Outcomes

**Decision**:
The recorded outcome for one Message in one Triage Run: the matching Tier and Rule, the Confidence and Reason if a Judge was consulted, and the Verdict.
_Avoid_: result, classification, action, outcome

**Verdict**:
What the Decision says should happen to the Message: `mark-read` or `leave`. Every scanned Message receives exactly one Verdict.
_Avoid_: action, label, category, status

**Mark read**:
The act of clearing a Message's unread state and applying the Read Audit Label. The only mutating action the bot performs in this context.
_Avoid_: read (ambiguous with scan), dismiss, archive, clear

**Leave**:
The act of applying the Left Audit Label to a Message and otherwise not touching it.
_Avoid_: skip, ignore, keep

**Audit Label**:
A Provider label the bot applies to every judged Message so a person can review its Decisions in the Provider and so later Triage Runs do not judge it again. There are two: Read and Left.
_Avoid_: tag, marker, processed flag

### Runs

**Triage Run**:
One scheduled execution of the bot across every Account. A Triage Run scans, decides and acts, then reports a summary.
_Avoid_: job, batch, cycle, sweep

**Scan**:
Fetching a Message's headers, snippet and stripped body for evaluation. Scanning changes nothing in the Provider.
_Avoid_: read, fetch, process

**Dry Run**:
A Triage Run that scans and decides but performs no mark-read or leave actions, reporting what it would have done.
_Avoid_: preview, test mode, simulation

**Fail Closed**:
The rule that when a Judge is unavailable, over quota or returns something unusable, the affected Candidates receive the Verdict leave.
_Avoid_: safe default, fallback

### Email Commands

**Email Command**:
A message an Account owner sends to their own plus-address (`local+triage@domain`), usually forwarding a Message, with a note saying how its Rules should change. Only a message Gmail marks as sent by the Account itself counts.
_Avoid_: email rule, bot email, instruction email

**Change**:
One Rule added by an Email Command. Changes only ever add to the rules file; they never replace or remove anything in it.
_Avoid_: edit, patch, override

**Overlay**:
The bot-owned set of Changes, kept in its own Secret and laid over the rules file at the start of every Triage Run.
_Avoid_: learned rules, extra rules, overrides file

**Protective Change**:
A Change that can only make the bot leave more mail alone: a Protect Rule or an Example whose Verdict is leave. Protective Changes apply without confirmation.
_Avoid_: safe change

**Proposal**:
The Changes from one Email Command that are not Protective, waiting for the owner to tap Apply or Discard in Telegram. A Proposal expires after seven days.
_Avoid_: pending change, request

**Interpreter**:
The component that turns an Email Command's note into proposed Changes. It never sees a forwarded Message's body, and every Change it proposes must be grounded in the note or the forwarded sender and subject.
_Avoid_: parser, assistant, Judge

### Archive

**Ref**:
A Message's stable short reference: 12 hex characters of a SHA-256 over its Account and Message-ID. The same in every run and report, and reveals nothing about the Message.
_Avoid_: checksum, hash, message key

**Archive**:
The durable record of Triage Runs and Email Command events, kept in a bucket on the cluster. Nothing reads it to make a Decision.
_Avoid_: backup, history store

### Archive

**Archive**:
The permanent record of every Triage Run's Decisions and every Email Command event, kept in a bucket. No Triage Run reads it.
_Avoid_: backup, history, store

**Ref**:
A Message's short, stable reference: 12 hex characters derived from its Account and Message-ID, the same in every Triage Run.
_Avoid_: id, hash, handle

**Backfilled Record**:
An Archive record rebuilt afterwards from logs rather than written by its Triage Run. It may have no Ref, Message-ID or snippet, and it always says so.
_Avoid_: imported record, legacy record

**Search**:
Looking up Archive records by Ref, sender, subject, Account, Verdict, Tier, Rule or date. Search only reads the Archive and never contacts the Provider, the Judge or Telegram.
_Avoid_: query, lookup, audit

### Tuning

**Decision Log**:
The complete set of Decisions from one Triage Run, including what each Message looked like when scanned, kept so a person can review it later.
_Avoid_: export, dump, output file

**Review**:
A person going through a Decision Log and agreeing or disagreeing with its Decisions, grouped by sender where the same answer applies to many Messages.
_Avoid_: audit, grading, labelling session

**Feedback**:
One person's agreement or disagreement with a Decision, recorded with the sender, subject and Verdict it concerned. Feedback is private and never lives in a repository.
_Avoid_: correction, annotation, vote, training data

**Example**:
A short description of a Message from a Brand together with its correct Verdict, attached to that Brand Rule to guide the Judge. Examples are written from Feedback and never contain message bodies.
_Avoid_: few-shot, sample, prompt example

**Replay**:
Re-evaluating the Messages in a Decision Log against the current Rules without contacting the Provider or the Judge, to show which Verdicts a Rule change would alter.
_Avoid_: re-run, simulation, backtest
