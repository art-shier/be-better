# Security check remediation — 2026-09-28

The Security workflow previously stopped at npm audit and Trivy installation. Once those failures were repaired, scans exposed vulnerable Go dependencies, toolchains, and binaries in the pinned runtime images.

## Changes

- Upgrade MDXEditor from 4.2.3 to its 4.2.5 patch release, which pins js-yaml 4.3.2. Keep the separate js-yaml v5 dependency. The earlier proposed npm override did not propagate across the workspace link with the installed npm version.
- Align Security with Node 24.15.0. Pin Trivy action v0.36.0 by commit and select scanner v0.74.0 explicitly. The action's nested installer is commit-pinned and checks release SHA-256 checksums; the scanner release asset digest was independently checked locally.
- Run govulncheck from each actual Go module directory. Use Go 1.26.8 consistently for application builds, CI, Release, and Docker; update x/crypto and moby/go-archive plus their required dependencies.
- Refresh digest-pinned Alpine, Caddy, and PostgreSQL images. Keep PostgreSQL on major version 17 and its existing data layout.
- Build Caddy 2.11.4 with its standard modules and separately locked, patched dependencies in `deploy/caddy`. The official image still embedded vulnerable dependency and standard-library versions. Replace its binary after removing the inherited bind-service capability.
- Rebuild the same upstream gosu 1.19 commit with Go 1.26.8, using the isolated tool module in `deploy/gosu`. Upgrade PostgreSQL image packages from its existing Alpine stable repositories. Check gosu privilege dropping in the image job.

The isolated Caddy/gosu modules use `GOWORK=off`; their go.mod/go.sum files are build inputs. Their vulnerability checks run in Security alongside the application. All five final images retain SPDX SBOM generation and blocking HIGH/CRITICAL scans, including unfixed findings. No scanner exceptions or failure suppression were added.

## Validation

Local validation used Node 24.15.0 and Go 1.26.8:

- npm audit: zero vulnerabilities; installed js-yaml versions 4.3.2 and 5.4.1.
- Web: 43 test files / 426 tests pass; TypeScript and production build pass. The existing large-chunk build warning remains.
- API: package tests, vet, and build pass. Docker-dependent cases are skipped locally because Docker is unavailable; CI runs the database/browser gates.
- Agent orchestration/contracts/architecture regressions: 68 pass. Release workflow/assets/validator regressions: 24 pass after adding Git Bash utilities to the Windows PATH; the initial run failed six cases because it selected Windows tools and lacked cygpath.
- Trivy filesystem scan of a clean source snapshot: zero HIGH/CRITICAL vulnerabilities or secret findings across the application, Caddy, gosu, and npm dependency manifests.
- govulncheck: no reachable vulnerabilities in the application, Caddy, or gosu. It still reports informational non-reachable dependency findings; those were not suppressed.
- Rebuilt Caddy validates the existing Caddyfile. Linux Caddy and gosu cross-compilation succeeds.

Final Linux container builds, all five image scans, the gosu execution check, and Compose TLS/runtime acceptance are enforced by the existing PR workflows. Their live results are linked from [PR #14](https://github.com/art-shier/be-better/pull/14). These checks do not enable the production Agent or exercise a real model provider.

## CI follow-up

Security run [36376375260](https://github.com/art-shier/be-better/actions/runs/36376375260) passed all six jobs, including the five final images and gosu execution. The functional CI race run exposed an overly specific shutdown-test message assertion: Gateway cancellation may persist `execution_interrupted` before Runtime completion attempts to persist `runtime interrupted`. Both paths preserve the first durable terminal state. The test now accepts these two interruption messages, additionally requires `retryable=false` and rejects a late Provider summary, and retains the no-replay and unknown-operation assertions. Production Agent behavior is unchanged.
