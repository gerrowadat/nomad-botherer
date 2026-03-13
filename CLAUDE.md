# nomad-botherer — Claude instructions

## Rules

**Always update tests.** Every code change must have corresponding test coverage. No exceptions.

**Always update docs.** Config flag added or changed? Update the README table. Behaviour changed? Update the relevant section. Keep docs current.

**Do not merge PRs.** Create the branch, commit, push, open the PR — then stop. Leave merging to the human.

**Write plain commit messages and PR descriptions.** Describe what changed and why. No superlatives, no "seamlessly", no "robust", no bullet-point sales pitches. A PR description should read like a code review, not a product announcement.

**Do not re-implement incumbents.** Before writing a library or utility from scratch, check whether a well-established Go package exists for it. "Well-established" means high GitHub stars and active maintenance. If something like `go-git`, `prometheus/client_golang`, or `hashicorp/nomad/api` already does the job, use it.

**Add Prometheus metrics for observable behaviour.** Any new operation that can fail, be counted, or be timed should have a corresponding counter or gauge registered in the Prometheus registry. Follow the existing pattern: register via `promauto.With(reg)` in the constructor, keep metric names under the `nomad_botherer_` prefix.

## Project layout

```
cmd/nomad-botherer/     entry point
internal/config/        flag + env config
internal/gitwatch/      in-memory git clone and polling
internal/nomad/         HCL parsing, Nomad diff logic
internal/server/        HTTP: /, /healthz, /diffs, /metrics, /webhook
```

## Key conventions

- All config flags have env var counterparts; document both in README
- Tests use injected interfaces (`NomadJobsClient`, `DiffSource`, etc.) — keep production code testable without a live Nomad cluster
- Per-test Prometheus registries (`prometheus.NewRegistry()`) to avoid duplicate-registration panics
- `/{$}` for exact root match (Go 1.22+ ServeMux)
