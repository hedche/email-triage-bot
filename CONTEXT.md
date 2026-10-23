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
