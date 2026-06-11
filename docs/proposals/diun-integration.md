# Proposal: image update tracking with Diun

**Status**: draft  
**Date**: 2026-06-11

> **Revision note**: an earlier draft of this proposal had nomad-botherer
> receiving Diun's webhooks, persisting "available update" records in Nomad
> Variables, and serving them from an API. That has been removed. A Diun
> notification is not actionable by nomad-botherer — nothing may change in
> Nomad until Git changes, and nomad-botherer already watches Git — so
> consuming notifications added state (the only state in the design not
> recomputable from Git and Nomad) without adding behaviour. Closing the
> notification-to-Git loop now lives explicitly outside the tool.

## Background

GitOps pins image tags in HCL. That is the point — the cluster runs what Git
says — but it means nobody is watching the other direction: upstream
publishes `api-server:1.44.0` and Git happily pins `1.43.0` forever. The
missing piece is *update availability*: knowing that a newer image exists for
something Git manages, and making it easy to act on.

[Diun](https://github.com/crazy-max/diun) (Docker Image Update Notifier,
crazy-max/diun) is the established tool for this: it watches container
registries for new or changed tags and sends notifications. Per the
no-reimplementation rule, nomad-botherer should not grow its own registry
polling; it should integrate with Diun.

The constraints that frame the design:

1. **Git is the source of truth for what to track.** The set of images worth
   watching is exactly the set referenced by managed jobs in the repo. That
   set should drive Diun, not be maintained by hand in a second place.
2. **nomad-botherer never writes to Git.** Not directly, not via the GitHub
   API, not on a side branch for this feature. Bumping a tag in a job file
   is a Git change like any other: it arrives by PR, authored by a human or
   by automation that is not this tool.
3. **nomad-botherer acts only on Git and Nomad state.** Everything it knows
   must be recomputable from those two (see "Restart safety and recovery" in
   [gitops-job-updates.md](gitops-job-updates.md)). Diun notifications are
   delivered exactly once and are not recomputable, so nomad-botherer does
   not consume them.

## Division of labour

```
nomad-botherer   Git HCL → derive the image watch list → expose it for Diun
Diun             check registries on its own schedule
                 → notify (Slack/Matrix/email/webhook/… — Diun's job)
outside          turn a notification into a Git PR: a human, or a small
                 separate "bumper" job — either may use nomad-botherer's
                 patch endpoint to get a ready-made diff
Git              PR review + merge: the only write path
nomad-botherer   ordinary GitOps apply of the merged commit (policy-gated,
                 see update-policies.md)
```

The circle still closes — Git's images stay current, and for jobs with
`gitops_update_policy = "image-only"` the merged bump is applied
automatically — but the segment that writes to Git is outside the tool, and
nomad-botherer's two roles in the circle are both stateless functions of
the repo at HEAD.

---

## What Diun provides (and does not)

Facts that constrain the design, from Diun's documentation:

- **Providers** define what Diun watches: Docker, Swarm, Kubernetes,
  **Nomad**, File, and Dockerfile. The Nomad provider connects to a cluster
  and watches images from running Docker-driver tasks, with opt-in via
  `diun.enable = "true"` in job/group/task meta (or service tags), plus a
  `watchByDefault` mode. The File provider reads a YAML list of image
  entries from the **local filesystem only** (a file or a directory; no
  HTTP fetch).
- **Per-image options** are the same vocabulary everywhere: `watch_repo`,
  `include_tags` / `exclude_tags` (regexps), `max_tags`, `sort_tags`
  (including `semver`), `notify_on` (`new`/`update`), `platform`, and a
  free-form `metadata` map that is echoed back in notifications — useful
  for whatever consumes them.
- **Notifications are push-only and once-only.** Diun has no HTTP query API;
  you cannot ask it "what updates are available?". It supports ~20
  notification channels (including a generic webhook), and each new tag or
  changed digest is notified **once** — Diun keeps its own seen-state in an
  embedded store. A consumer that loses a notification does not get it
  again. This is the property that makes notification consumption a poor
  fit for a tool whose state must be recomputable.
- It **never writes** to a cluster or a repo, which is exactly why it
  composes with a GitOps operator instead of competing with one.

---

## Who owns the watch list

Diun wants a list it owns and polls; it does not answer ad-hoc queries. The
question is where that list comes from.

### Alternative A: Diun's Nomad provider, nomad-botherer uninvolved

Point Diun's Nomad provider at the cluster. Jobs opt in by carrying
`diun.enable = "true"` in their meta — which, for managed jobs, lives in the
HCL in Git, so the opt-in is still version-controlled. nomad-botherer has no
role at all.

**Pros**

- Zero code. Diun's Nomad provider already exists and is maintained.
- The `diun.*` meta keys in HCL are human-written and round-trip through
  registration untouched — no meta-drift.
- Diun's default Nomad metadata (job ID, namespace, task group) is included
  in notifications, giving the consumer job context for free.

**Cons**

- **The watch list is the *running cluster*, not Git.** During drift windows
  the tracked tag is whatever is running, not what Git declares; a job
  committed to Git but not yet registered is not watched at all; a job
  stopped for maintenance silently drops out of tracking.
- Diun needs its own Nomad token with job-read access, a second credential
  to manage.
- Only Docker-driver tasks of *running* jobs are seen.

### Alternative B: nomad-botherer generates the File provider list

nomad-botherer already parses every managed job's HCL each cycle. From the
parsed jobs it derives the image watch list: every Docker task image in a
job with `"diun.enable" = "true"` in meta, with the job's other `diun.*`
meta keys mapped onto the corresponding File provider entry options
(`include_tags`, `sort_tags`, `watch_repo`, …). Each entry's `metadata` map
carries the owning job IDs and HCL file paths, so notifications arrive at
their consumer pre-correlated with the repo.

The list is exposed two ways, both stateless and regenerated from HEAD:

- `GET /api/v1/diun/images.yml` — the rendered File provider YAML, always
  available. Useful for inspection and for any external mechanism that
  delivers it to Diun.
- `--diun-image-list-path` / `DIUN_IMAGE_LIST_PATH` (optional): a filesystem
  path the list is (atomically: write temp + rename) rewritten to whenever
  HEAD changes. Intended for co-scheduling Diun and nomad-botherer in the
  same Nomad task group with a shared `alloc/` directory; Diun's File
  provider points at the shared path. This is a deliberate, narrow exception
  to the no-disk-writes posture: the file is derived state, owned by this
  feature, regenerable from Git at any time, and written outside the in-memory
  git clone (which stays `memory.NewStorage()`).

**Pros**

- **Git is the source of truth**, exactly as required. Images are tracked
  from the moment they are committed, before first registration, and
  independently of cluster state.
- The same `diun.*` meta vocabulary as Alternative A — switching between
  alternatives needs no HCL changes.
- Diun needs no Nomad credentials.

**Cons**

- The File provider reads local files only, so list delivery requires either
  co-scheduling with a shared alloc dir or an external fetch-to-disk step.
- More code in nomad-botherer: list rendering, the endpoint, the file
  writer, and their tests.

### Alternative C: query Diun directly — rejected

Diun has a gRPC API used by its own CLI (`diun image list`), bound to
localhost by default. It is an internal interface, not a stable integration
surface, and polling it would still leave Diun's watch list to be maintained
somewhere. This alternative is noted only because "nomad-botherer asks Diun"
sounds plausible until the shape of Diun's API is examined.

### Verdict

Alternative B. Alternative A remains a valid zero-code deployment for setups
that don't care about the drift-window and not-yet-registered caveats; since
nomad-botherer is uninvolved in notification consumption either way, nothing
in this tool needs to know which alternative a deployment chose.

---

## The patch helper

nomad-botherer holds one thing the notification consumer wants and cannot
cheaply get: the parsed repo at HEAD, including which managed HCL files
reference a given image repository and the exact literal to substitute. The
patch helper exposes that as a stateless endpoint — the caller brings the
facts from the notification, nomad-botherer renders the diff:

```
GET /api/v1/image-patch?repository=ghcr.io/example/api-server&tag=1.44.0
```

returns `text/x-patch`: a unified diff against the repo at current HEAD that
bumps every managed job's reference to that repository up to the given tag
(possibly spanning multiple files, when several jobs pin the same image).
An optional `&job=<job_id>` narrows it to one job's file.

```diff
--- a/jobs/api-server.hcl
+++ b/jobs/api-server.hcl
@@ -23,7 +23,7 @@
       driver = "docker"
       config {
-        image = "ghcr.io/example/api-server:1.43.0"
+        image = "ghcr.io/example/api-server:1.44.0"
       }
```

Implementation notes:

- The patch is produced by **exact string substitution** of the old image
  reference in the raw file content from the in-memory clone — not by
  re-rendering parsed HCL, which would destroy formatting and comments. The
  unified diff is generated with an established diff library, not
  hand-rolled.
- The endpoint is a pure function of (HEAD, repository, tag). No notification
  state, nothing to persist, nothing to lose on restart.
- `404` when no managed job references the repository; `422` when a
  referencing file builds the image string from HCL variables or
  interpolation, with a body naming the file — substitution cannot work
  there, and the limitation is documented rather than worked around.
- The tag is taken on faith. Validating that it exists in the registry would
  mean nomad-botherer talking to registries, which is Diun's job; the caller
  got the tag *from* Diun.

The endpoint deliberately stops one step short of a PR. nomad-botherer never
holds GitHub write credentials.

---

## Closing the loop, outside nomad-botherer

How a Diun notification becomes a Git PR is out of scope for this tool, by
design. Two shapes, for illustration:

**Manually.** Diun notifies a Slack/Matrix/email channel. A human sees
"api-server 1.44.0 available" and runs:

```
curl -s "http://botherer:8080/api/v1/image-patch?repository=ghcr.io/example/api-server&tag=1.44.0" \
  | git apply
git checkout -b bump-api-server
git commit -am "Bump api-server to 1.44.0" && git push
```

**A separate bumper job.** A small service (or scheduled Nomad job — *not*
nomad-botherer, not this repo) receives Diun's generic webhook, calls the
patch endpoint with the notified repository and tag, and opens a PR via the
GitHub API. It owns the GitHub token, its own auth on the webhook, and its
own policy about which notifications deserve automatic PRs. If it dies and
loses a notification, that is between it and Diun — nomad-botherer's
correctness and state are untouched. Tools like Renovate occupy the same
seat (see [prior-art.md](../prior-art.md)).

Either way, the merged PR is ordinary Git drift and flows through the apply
path under the job's [update policy](update-policies.md) — for jobs set to
`image-only`, this circle is precisely the automation they opted into.

---

## Observability

Following the metrics convention (`promauto.With(reg)`, `nomad_botherer_`
prefix):

- `nomad_botherer_diun_image_list_entries` — gauge; entries in the generated
  watch list.
- `nomad_botherer_diun_image_list_writes_total` / `…_write_errors_total` —
  counters; rewrites of the `--diun-image-list-path` file.
- `nomad_botherer_image_patches_served_total` — counter.
- `nomad_botherer_image_patch_errors_total{reason}` — counter; `reason` of
  `unknown_repository` or `non_literal_reference`.

---

## Deployment sketch (Alternative B, co-scheduled)

One Nomad job, one group, two tasks sharing the allocation directory:

- `nomad-botherer` with
  `--diun-image-list-path=${NOMAD_ALLOC_DIR}/data/diun/images.yml`.
- `diun` with the File provider pointed at that path, its embedded state db
  on ephemeral task disk — losing it merely re-notifies everything once,
  which is the notification consumer's noise to absorb, not
  nomad-botherer's — and notifiers configured for wherever the loop is
  closed (a chat channel for the manual workflow, the bumper job's endpoint
  for the automated one).

No volumes, no external services; the whole pair is reschedulable to any
node, preserving the no-volume-claims principle.

---

## Open questions

- **Tag constraint defaults.** Should jobs without `diun.include_tags` get a
  generated default (e.g. `sort_tags: semver` + `watch_repo: true`), or the
  Diun defaults? Generated defaults are friendlier but more magic.
- **Shared images with conflicting constraints.** Two jobs watching the same
  repository with different `diun.*` options need either merged constraints
  in one File entry or one entry per distinct (repository, constraints)
  pair. The latter is simpler; verify Diun deduplicates registry calls
  itself.
- **Digest-only re-pushes.** Diun's `status: update` (same tag, new digest)
  has no HCL expression — the file already names the right tag — so the
  patch endpoint can do nothing with it. It is still a signal worth routing
  somewhere (a mutable pinned tag changed under you), but that routing is
  the notification consumer's concern.
- **List staleness on the shared-file path.** If nomad-botherer is down, the
  last-written list file persists and Diun keeps watching a stale set. That
  is benign (it is at worst yesterday's truth) but worth a line in the
  README when implemented.
