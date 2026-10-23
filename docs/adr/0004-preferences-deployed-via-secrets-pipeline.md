---
status: accepted
---

# Rules and accounts are deployed as Secrets through the Nature secrets pipeline

The rules file and the accounts file are both stored as block-scalar entries of the `email-triage-rules` and `email-triage-accounts` Secrets in the Nature secrets file, which lives outside every repository, and are pushed to the cluster with Nature's `scripts/secrets.sh`. The accounts file holds Gmail refresh tokens and is a credential by any reading; the rules file holds no credential but names protected people, the brands a household buys from and the addresses it hears from, which is exactly the personal detail a public repository must not carry.

## Considered options

- **ConfigMap rendered from Nature via `configMapGenerator`.** The obvious home for non-credential configuration, and rejected because Nature is public: the rules file would publish protected addresses, domains and brand relationships to anyone reading the GitOps repo.
- **A private config repository pulled by Flux.** Works, and keeps the rules versioned, but adds a second repository, a second deploy key and a second review path for a file two Secrets already carry — and the tuning skill would then need a commit-and-push cycle to change a threshold.
- **SOPS-encrypted manifests in Nature.** Keeps history in git, but the cluster has no existing SOPS/age flow, and encrypted YAML is not something a person edits during a tuning session.

## Consequences

- Rules are not versioned in git. The only history is whatever the secrets file's own backups hold; a bad edit is undone by editing again, not by reverting a commit.
- The `triage-tune` skill edits the secrets file in place with `yq` and pushes with `scripts/secrets.sh push`, so tuning never touches a repository and never writes personal addresses into one.
- A future reader will find a plain YAML configuration file living inside a Kubernetes Secret and be tempted to "fix" it into a ConfigMap. That is this decision, not an oversight: the rules file is not secret because it holds credentials, it is secret because it is personal.
