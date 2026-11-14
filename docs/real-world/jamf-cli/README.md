---
layout: default
title: "jamf-cli — order-dependent test (issue #370)"
---

# jamf-cli: an order-dependent test, shrunk to one polluter

Repo: [`Jamf-Concepts/jamf-cli`](https://github.com/Jamf-Concepts/jamf-cli) at
`977b704`, 2026-11-11. Issue
[#370](https://github.com/Jamf-Concepts/jamf-cli/issues/370), open since
2026-09-07. The fix merged on November 12, 2026 in
[#474](https://github.com/Jamf-Concepts/jamf-cli/pull/474)
(`62feabf4168c816114218b94f932e9aecac249aa`).

**Attribution.** The maintainer (`ktn-jamf`) wrote the issue and named the
mechanism: the package keeps cobra's parsed state in package-level vars, so any
test that drives the root command leaves it set. The contribution here is the
minimal polluter, the deterministic witness, and the fix — not the diagnosis.

## Why this repository

Not a seeded exercise: a real product, pushed the same day the hunt ran, with an
admitted order dependence that had been open for two months. The package has
1736 tests and a full run costs 60-200 s, which is why nobody had shrunk it by
hand.

## Reproduction at the base commit

```sh
go test ./internal/commands/ -count=1               # ok, 61s
go test ./internal/commands/ -count=1 -shuffle=10   # FAIL: the victim
go test ./internal/commands/ -count=1 -shuffle=1234 # FAIL: the victim
```

The seeds named in #370 are partly stale: `-shuffle=2` and `-shuffle=7` pass on
`977b704`, so the failure reproduces on 10 and 1234.

## Finding the polluter

`repro --from-json` on the failing `-shuffle=10` log (38 MB; the victim ran at
index 1873 of 3146) shrank 1873 preceding tests to **one**:

```
flake found: TestCheckAPIMatchRefusesAPlatformCommandOnAnInstanceProfile
  package      : github.com/Jamf-Concepts/jamf-cli/internal/commands
  class        : order
  polluter     : TestResolveAuth_URLNormalization
  run          : 3/3 failed
  Wilson 95%   : 43.9%–100.0%
  deterministic: yes
  capsule      : gfh_e628d198b8b2
  status       : confirmed
```

Files in this directory:

| File | What it is |
|---|---|
| [`capsules/gfh_e628d198b8b2.json`](capsules/gfh_e628d198b8b2.json) | the capsule; `repo.dirty` is `false`, base commit `977b704` |
| [`repro.txt`](repro.txt) | the `repro --from-json` run that produced it (the checkout's absolute path is replaced by `<repo>`; nothing else is edited) |
| [`shuffle-before.txt`](shuffle-before.txt) | seed sweep on pristine `977b704` |
| [`shuffle-after.txt`](shuffle-after.txt) | the same sweep with the patch |
| [`shuffle-seed7-alone.txt`](shuffle-seed7-alone.txt) | the A/B that ruled out a load-induced failure |

`compile --verify` wrote the regression file and reported
`verification: RED (verified — the flake is still there)`.

## Mechanism

`credentialSource` (`internal/commands/gateway_coverage.go:171`) builds the
sentence the victim asserts on from four package-level vars: `token`,
`tokenFile`, `clientID`, `clientSecret`.

`resolveAuth` (`internal/commands/root.go:703-734`) **writes** them: it caches
`JAMF_TOKEN`, `JAMF_CLIENT_ID`, `JAMF_CLIENT_SECRET` and token-file contents into
the globals. Every test that calls `resolveAuth` therefore leaves them set.

- Polluter `TestResolveAuth_URLNormalization` resolves a profile whose token is
  `env:TEST_URL_NORM_TOKEN`, which sets `token`.
- Victim asserts the refusal names `profile "pro-instance"`; with `token` set it
  reads `the JAMF_TOKEN environment variable` instead:

```
gateway_coverage_test.go:95: the refusal does not mention "profile \"pro-instance\"":
  ... (auth-method oauth2, from the JAMF_TOKEN environment variable)
```

`tokenFile` leaks the same way through `TestResolveAuth_TokenFromFile`
(`the token in <path>`), and so do `clientID`/`clientSecret`.

In source order the two assertions run before any polluter
(`gateway_coverage_test.go` sorts before `pro_*` and `root_test.go`), which is
why the default suite is green and CI never shuffled.

## Fix

`isolateAuthGlobals(t)` already exists in `platform_scope_flags_test.go` — added
in [#410](https://github.com/Jamf-Concepts/jamf-cli/pull/410) for this exact
failure mode. The two tests that assert on `credentialSource` output now call
it. 11 lines, one test file, no production change.

## Verified

| Run | before (`977b704`) | with the patch |
|---|---|---|
| `-count=1` | ok | ok |
| `-shuffle=2` | ok | ok |
| `-shuffle=7` | ok | ok |
| `-shuffle=10` | **FAIL** | ok |
| `-shuffle=1234` | **FAIL** | ok |

A witness built from the capsule (polluter → victim, build-tagged `go-flake-hunter`)
is RED before and GREEN after.

One measurement lesson: an early "seed 7 fails" reading was taken while a second
full suite ran on the same machine and did not reproduce when run alone — see
`shuffle-seed7-alone.txt`. A shuffled run is deterministic in *order* but not in
*wall clock*, and this package has tests sensitive to the latter.

## Still order-dependent (measured, untouched)

| Seed | Failing test |
|---|---|
| `-shuffle=99991` | `TestProGroupAction_UniqueGroupNameSendsOneWriteThroughTheRoot` |
| `-shuffle=on` (random) | `TestDryRun_ProUploadIsNeverSent` |

Each needs its own polluter before a fix can be trusted, so they are left out of
the PR.
