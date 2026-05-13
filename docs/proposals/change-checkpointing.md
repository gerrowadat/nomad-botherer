# Proposal: checkpointing ongoing job updates

**Status**: draft  
**Date**: 2026-05-13

## Background

The async update queue described in the GitOps job updates proposal is
in-memory. When nomad-botherer restarts — upgrade, crash, or eviction — any
updates that were `PENDING` or `IN_PROGRESS` are lost. The next diff cycle
recreates them, so correctness is not compromised, but there is a window where
an apply was already sent to Nomad but the outcome was never recorded, and a
second apply of the same change can occur.

For idempotent operations (re-registering an already-registered job with the
same content) this double-apply is harmless. For `DEREGISTER` operations it
could be a problem if the job was re-registered between the first apply and
the restart; the second apply could delete a job that should be running.

The more significant problem is durability of intent. If a long-running
multi-job rollout (e.g., deploying 30 jobs from a single commit) is interrupted
halfway, the operator has no record of which jobs were applied and which were
not. A fresh diff cycle will detect the remaining drift and queue new updates,
but whether those new updates correspond to the same intent as the interrupted
rollout is ambiguous.

This proposal describes three ways to checkpoint update state without a
standalone database.

---

## Requirements

- Survive a nomad-botherer process restart without losing knowledge of which
  updates were applied and which are still pending.
- Resume a partial rollout rather than re-deriving intent from scratch on every
  restart.
- Not require an external database (PostgreSQL, Redis, etcd, etc.).
- Ideally, not require additional infrastructure beyond what the service already
  talks to (Nomad, Git).

---

## Alternative 1: Nomad Variables as the checkpoint store

**How it works**

Nomad 1.4+ includes a built-in key-value store called Nomad Variables, backed
by Raft and replicated across the cluster. Variables have ACL integration,
support CAS (check-and-set via `ModifyIndex`), and survive cluster restarts.

nomad-botherer writes one Variable per in-flight rollout at a well-known path:

```
nomad/jobs/gitops/checkpoints/<git_commit>
```

The value is a serialised (JSON or protobuf binary) snapshot of the `JobUpdate`
slice for that commit. The Variable is created when the first update for a
commit is enqueued and updated atomically as each update transitions through
`PENDING → IN_PROGRESS → SUCCEEDED/FAILED`. When all updates for a commit reach
a terminal state, the Variable is deleted (or left for audit; configurable).

On startup, nomad-botherer reads all Variables under
`nomad/jobs/gitops/checkpoints/` and rehydrates the in-memory queue from any
non-terminal updates. Updates that were `IN_PROGRESS` are reset to `PENDING`
and retried (the CAS token from `JobModifyIndex` prevents double-apply harm).

**Interaction with Nomad Raft index**

Nomad Variables use the same Raft log as job state. A Variable write returns a
`ModifyIndex` that can be used for CAS on the next update, ensuring that two
concurrent nomad-botherer instances (e.g., during a rolling upgrade) cannot
write conflicting checkpoint data. The instance that loses the CAS retries after
re-reading the Variable.

**Implementation sketch**

```go
type CheckpointStore interface {
    // Write atomically updates the checkpoint for a commit.
    // modifyIndex is 0 for a new checkpoint, or the previous ModifyIndex.
    Write(ctx context.Context, commit string, updates []JobUpdate, modifyIndex uint64) (uint64, error)

    // Read returns the checkpoint for a commit, or nil if none exists.
    Read(ctx context.Context, commit string) ([]JobUpdate, uint64, error)

    // List returns all active checkpoints.
    List(ctx context.Context) (map[string][]JobUpdate, error)

    // Delete removes a checkpoint once a rollout is complete.
    Delete(ctx context.Context, commit string) error
}
```

The Nomad client already exists in the process; this adds usage of
`client.Variables()` from the same `github.com/hashicorp/nomad/api` package.

**Pros**

- No new infrastructure. Nomad is already a hard dependency.
- Raft-backed durability and replication match the cluster's own guarantees.
- CAS prevents split-brain between concurrent nomad-botherer instances.
- ACLs on Variable paths can restrict who can read or modify checkpoint state.
- Nomad's built-in UI shows Variables; operators can inspect checkpoints without
  extra tooling.

**Cons**

- Requires Nomad 1.4+ (Variables API). Older clusters cannot use this approach.
- Adds a new write path to Nomad for operational state, which may conflict with
  cluster ACL policies that restrict writes to the `nomad/jobs/` namespace.
- Variable size limit is 64 KiB per key. A very large rollout (hundreds of jobs)
  may exceed this; mitigation is one Variable per job rather than per commit,
  at the cost of more API calls on startup.
- Nomad Variables are not designed as a queue and have no watch/notify semantics;
  polling is required.

**Verdict**: the cleanest option when Nomad 1.4+ is available. Keeps all
operational state inside the system being managed.

---

## Alternative 2: Git branch as the checkpoint store

**How it works**

nomad-botherer maintains a dedicated branch in the same repository it watches,
e.g., `gitops-state`, which it treats as a write-only append log. The branch
holds one file per active rollout:

```
checkpoints/<git_commit>.json
```

Each file contains the `JobUpdate` slice for that commit, serialised as JSON.
The file is committed and pushed when updates are enqueued, and updated with
terminal statuses when each update completes. The branch is never merged into
the main branch; it is purely operational state.

On startup, nomad-botherer shallow-fetches the `gitops-state` branch (a small
fetch since it only contains checkpoint files, not job HCL), reads all
non-terminal checkpoint files, and rehydrates the queue.

**Concurrency control**

Git itself provides concurrency control via push rejection. If two instances try
to push a checkpoint update simultaneously, one will receive a non-fast-forward
rejection and must pull, merge, and retry. For checkpoint files (one file per
commit, independent between commits), merge conflicts are essentially impossible;
the only conflict would be two instances updating the same file, which is
prevented by the single-writer design (only the instance that detected the
commit owns its checkpoint).

**Implementation sketch**

The existing `gitwatch.Watcher` uses `go-git` and `memory.NewStorage()`. The
checkpoint writer needs a separate, persistable storage (not in-memory) so that
pushes can be made. This likely means a second `go-git` clone with disk-backed
storage, or a thin wrapper around `git` CLI calls for the state branch.

```go
type GitCheckpointStore struct {
    repoURL    string
    branch     string  // "gitops-state"
    workDir    string  // disk path for the state clone
    auth       transport.AuthMethod
}
```

**Pros**

- Git is already a dependency; no new credentials or network endpoints needed
  (assuming write access to the repo is already granted via token or SSH key).
- The checkpoint history is a full Git log: every state transition is an
  immutable commit, with timestamp and message. This is a better audit trail
  than the in-memory or Nomad Variables approaches.
- Standard Git tooling (`git log`, `git diff`, `git show`) lets operators
  inspect and manipulate checkpoint state without custom tooling.
- No external API version constraints (works with any Git host).

**Cons**

- Requires write access to the repository. Read-only tokens (common for
  pull-based GitOps setups) are not sufficient.
- Adds Git push latency to the hot path of every status update. A rollout of
  30 jobs produces 30+ commits to the state branch.
- Branch history grows unboundedly unless a periodic cleanup job prunes old
  checkpoint commits (e.g., `git push --force` with a truncated history, or
  a separate cleanup cron).
- Mixing operational state and source code in one repository is operationally
  awkward. Teams that have separate read/write access policies for source vs
  operational state cannot use this without repo restructuring.
- The state branch must be protected from human pushes that could corrupt
  checkpoint data; this requires branch protection rules on the Git host.

**Verdict**: good for teams that already have write access and want a full audit
trail, but the per-update commit overhead and the read-write access requirement
make it awkward as a default.

---

## Alternative 3: Nomad job meta as distributed per-job state

**How it works**

Rather than storing a single centralised checkpoint, nomad-botherer records the
GitOps state of each job *on the job itself* using Nomad's `Meta` map. Every
registered job has a key-value `Meta` field that is stored in Nomad's state and
returned with `Jobs.Info()`. nomad-botherer writes to this field when applying
an update:

```json
{
  "Meta": {
    "gitops.commit":      "abc1234def5678",
    "gitops.hcl_path":    "jobs/api-server.hcl",
    "gitops.applied_at":  "2026-05-13T10:00:00Z",
    "gitops.status":      "succeeded"
  }
}
```

On startup, nomad-botherer calls `Jobs.List()` and then `Jobs.Info()` for each
job, reads the `gitops.*` meta keys, and reconstructs which jobs have already
been reconciled to which commit. Jobs whose `gitops.commit` matches the current
Git HEAD and whose `gitops.status` is `succeeded` do not need to be re-applied;
the diff check will confirm this independently via `EnforceIndex`.

There is no checkpoint file. State is entirely distributed across the jobs
themselves.

**Handling deregistered jobs**

For jobs that were deregistered (`missing_from_hcl`), there is no job record to
write meta to. The apply removes the job entirely, so there is nothing to read
on restart. This is acceptable: on restart, `Jobs.List()` will not return the
deregistered job, and the diff check will confirm it is absent from both Git and
Nomad, so no action is taken.

**Implementation sketch**

When calling `Jobs.Register()` for a GitOps update, merge the `gitops.*` meta
keys into the job's `Meta` map before sending:

```go
func applyMeta(job *nomadapi.Job, update JobUpdate) {
    if job.Meta == nil {
        job.Meta = make(map[string]string)
    }
    job.Meta["gitops.commit"] = update.GitCommit
    job.Meta["gitops.hcl_path"] = update.HCLFile
    job.Meta["gitops.applied_at"] = time.Now().UTC().Format(time.RFC3339)
    job.Meta["gitops.status"] = "succeeded"
}
```

On startup, the reconstruction scan is a single `Jobs.List()` plus N
`Jobs.Info()` calls, which nomad-botherer already makes for its first diff
check. The restart cost is zero additional API calls.

**Pros**

- Truly zero new infrastructure. State is stored in Nomad's existing job
  records; no Variables API, no Git writes, no files on disk.
- The checkpoint scan on startup reuses the same `Jobs.Info()` calls that the
  first diff check makes anyway, so restart cost is exactly one diff cycle.
- Meta fields are visible in the Nomad UI and `nomad job status` output,
  so the GitOps commit and apply time are self-documenting on the job.
- Works with all Nomad versions (Meta has been present since early Nomad).
- No concurrency concerns: meta is written atomically as part of job
  registration, using the same `EnforceIndex` CAS that prevents double-apply.

**Cons**

- Modifies the job's `Meta` map on every GitOps apply. If the HCL file defines
  its own `meta` stanza, the GitOps keys must be merged without overwriting
  user-defined keys. This requires care in the merge logic.
- The next diff check will detect the `gitops.*` meta changes as a "modified"
  diff if the HCL file does not include those keys, creating a spurious diff
  loop. Mitigation: the differ must strip `gitops.*` meta keys from the Nomad
  job before comparing with HCL, or the HCL author must include placeholder
  values.
- State for `IN_PROGRESS` updates is not stored at all; only `succeeded` is
  written (after the fact). An `IN_PROGRESS` update that is interrupted by a
  restart is re-derived from the diff on the next cycle, but the operator has
  no record that an apply was attempted.
- Deregistered jobs leave no trace. If an operator later wonders why a job is
  not running, there is no meta record explaining that it was intentionally
  removed by GitOps.
- Meta values are strings; structured data (e.g., a list of failed attempts)
  requires JSON encoding within the string value, which is fragile.

**Verdict**: the most elegant option in terms of zero infrastructure, but the
spurious-diff problem (alternatives: ignore-list logic in `Check()`, or require
HCL authors to include meta stanza) adds non-trivial complexity to the differ.
Best suited to environments where Nomad Variables are not available.

---

## Comparison

| Property | Nomad Variables | Git state branch | Job meta |
|---|---|---|---|
| Infrastructure | Nomad 1.4+ | Git write access | Any Nomad |
| Durability | Raft-backed | Git history | Nomad job store |
| Audit trail | Moderate (Variable history) | Best (full Git log) | Minimal (current meta only) |
| Startup cost | List Variables + rehydrate | Clone state branch + read files | Reuses diff-cycle Info() calls |
| Concurrent instances | CAS on Variable ModifyIndex | Push rejection + retry | CAS on JobModifyIndex |
| Diff loop risk | None | None | Requires differ to strip gitops.* keys |
| Deregister tracking | Variable deleted on success | Checkpoint file deleted | No record |
| Nomad version required | 1.4+ | Any | Any |

---

## Recommended path

Implement Alternative 1 (Nomad Variables) as the default, gated behind a config
flag (`--checkpoint-store`, default: `nomad-variables`). Add Alternative 3 (job
meta) as a fallback for Nomad versions before 1.4, selectable via
`--checkpoint-store=job-meta`. Alternative 2 (Git branch) is available as an
opt-in for teams that want the full audit trail.

The `CheckpointStore` interface above is the right abstraction boundary. Each
alternative is an implementation of that interface; the update queue does not
need to know which backend is active.

---

## Open questions

- **Variable path prefix**: should the path be configurable
  (`--nomad-variable-prefix`) to allow multiple nomad-botherer instances
  managing different namespaces to coexist without collision?
- **Cleanup policy**: should terminal checkpoints be deleted immediately or
  retained for a configurable duration (e.g., `--checkpoint-retention 24h`)
  for post-hoc debugging?
- **IN_PROGRESS fence**: if a Variable shows `IN_PROGRESS` on startup (the
  previous instance crashed mid-apply), should nomad-botherer immediately
  retry, wait one diff interval, or require manual intervention? Retrying is
  safe due to CAS, but may surprise operators who want to inspect state before
  resuming.
