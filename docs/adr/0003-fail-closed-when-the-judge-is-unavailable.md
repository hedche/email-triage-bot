---
status: accepted
---

# Candidates are left unread when the Judge cannot answer

If the Judge errors, times out, is over quota, or returns output that fails validation (missing a Candidate, malformed JSON, Confidence out of range), every affected Candidate receives the Verdict leave and the run is reported as failed. There is no fallback to a second Judge and no default of marking read. A wrongly unread Message costs a glance; a wrongly read one can hide something that mattered, and a silent fallback could send mail content to a Provider the operator did not choose for that run.

## Consequences

- A Judge outage shows up as a failed Job and a Telegram summary with an error count, not as a quiet morning.
- Candidates left this way are not given the Left Audit Label, so the next run judges them again once the Judge is back.
