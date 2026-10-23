---
status: accepted
---

# Stateless triage with Provider labels as the only state

The bot keeps no database and mounts no volume. The unread query in the Provider is the work queue, and the two Audit Labels (Read and Left) record which Messages have already been judged so they are excluded from the next run's query. Decision history lives only in the structured logs shipped to Loki.

## Considered options

- **JSONL or SQLite on a PVC.** The homelab precedent (peanut) does this and its manifests document the cost: a ReadWriteOnce Ceph volume forces `Recreate` rollouts, pod affinity between workloads, init containers asserting ownership, and a class of silent write failures. A once-a-day CronJob gains nothing from it.
- **Stateless without labels.** Every unread Message the Judge chose to leave would be re-sent to the Judge every morning, burning quota and re-exposing the same content daily.

## Consequences

- Reviewing or undoing the bot's work is a label search in the mail client, which is more useful to a person than a database row.
- Anything that needs memory across runs (the planned delivery-tracking phase, learned sender preferences) will need this decision revisited, most likely with a small file on a PVC or a Provider label carrying the state.
- Clearing the Audit Labels in the Provider resets the bot; that is a feature.
