---
status: accepted
---

# Triage results are archived in a Ceph bucket, and every Message has a Ref

After every Triage Run the bot writes its Decision Log and summary to the `email-triage-archive` bucket on cereal's Ceph object store (`runs/YYYY/MM/DD/<run_id>.jsonl.gz` and `.summary.json`), and the `listen` pod writes Email Command events to `commands/YYYY/MM/DD/`. Records are kept forever and hold what the Decision Log holds: sender, subject, Gmail's preview snippet and the Decision, never the body. Every Message carries a Ref: the first 12 hex characters of `sha256(account + "\n" + Message-ID)`.

## Why

- **A Ref from the Message-ID, not from content.** The Message-ID header is fixed when the mail is sent, so a Ref is the same in every run. A content hash would collide for identical newsletters and change if a body were re-rendered. Including the Account gives the same mail in two mailboxes two Refs, because the bot acts per mailbox. The record keeps the Message-ID, so a Gmail search for `rfc822msgid:` opens the mail. A Message with no Message-ID falls back to its provider ID.
- **Ceph, not Loki or a volume.** Loki is for searching logs and is not a record. An ObjectBucketClaim is the pattern cereal already uses, stays on the cluster network and needs no volume mounted on a Job.
- **A small signed PUT, not an SDK.** The bot only ever writes single objects; Signature V4 for one PUT is a page of standard library.
- **Best-effort.** A failed archive write is logged and reported in Telegram, and never fails the run or the command it records. ADR 0001 still holds: no Triage Run reads the archive, and labels remain the only state a run depends on.
- A decompressed run file is an ordinary Decision Log, so `replay` works on any past run.
