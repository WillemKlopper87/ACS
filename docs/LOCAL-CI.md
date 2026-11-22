# Running the checks without GitHub Actions

`.github/workflows/ci.yml` needs GitHub Actions minutes. When they are not
available (the account's spending limit is reached, or a job reports "not
started because recent account payments have failed"), jobs fail without
running and say nothing about the code. `scripts/ci-local.sh` runs the same
checks on a machine you control, so a change can still be checked before it is
merged.

```bash
scripts/ci-local.sh                       # go, migrate, spec, frontend, vuln
scripts/ci-local.sh --only go,migrate     # some stages
scripts/ci-local.sh --images --secrets    # also build and scan the images, and run gitleaks
scripts/ci-local.sh --e2e                 # also the Playwright browser tests
```

## What it runs

| Stage | Checks (same as CI) |
|---|---|
| `go` | `gofmt`, `go vet`, `go build`, `go test -p 1` (with `-race` when a C compiler is installed) against a throwaway Postgres |
| `migrate` | migrate a clean database, run it again, `-check`, then three concurrent runners |
| `spec` | Redocly lint of both OpenAPI files; the generated frontend types match the spec |
| `frontend` | `npm ci`, lint, typecheck and build, unit tests, `npm audit --omit=dev` |
| `vuln` | `govulncheck` at the version CI pins |
| `--images` | build the five images and scan each with Trivy (HIGH and CRITICAL with a fix) |
| `--secrets` | gitleaks over the working tree |

It uses the toolchains CI uses, not the ones installed: the Go version in
`backend/go.mod` (through `GOTOOLCHAIN`) and Node 22 for the unit tests. A
different Go changes what `govulncheck` reports, and Node 26 breaks one
`localStorage` test. The first run downloads those toolchains.

## What it does not run

The USP interop job against the obuspa reference agent, and the `field-rc`
host-service and upgrade rehearsals. Run those on the lab host when a change
touches the device paths, or leave a change that touches them for a day when
Actions is available again.

## Needs

Docker (a throwaway Postgres container named `acs-ci-pg` on `127.0.0.1:55440`;
nothing else is touched, and never the lab database or the secrets file), Go,
and Node. A full run takes about 20 minutes on a laptop, most of it the Go
tests.

## Merging without CI

Run the script on the branch, read the summary, and merge only when every
selected stage passes. Say which stages ran in the pull request. A change to a
container image or a GitHub Actions version is checked by `--images`, or not at
all: an Actions bump cannot be tested outside Actions.
