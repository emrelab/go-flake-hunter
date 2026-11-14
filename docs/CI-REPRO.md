---
layout: default
title: CI reproduction recipe
---

# CI recipe: a red test log → witness → job summary

The pieces already ship; this page is the glue between them (#17).

```
red `go test -json` log  →  go-flake-hunter repro --from-json  →  witness  →  job summary
```

The point is that the developer pastes nothing. CI already paid for the discovery
(the log names the failing tests); `repro` only has to shrink them.

**Rules the recipe keeps, because the tool's honesty depends on them:**

- **Post only on exit `0`.** Exit `3` means "did not reproduce within budget" and
  exit `4` means "budget exhausted" — in both cases there is no witness to show,
  and a summary saying "found a flake" would be a lie. Silence is the answer.
- **Read-only token.** The same job summary works for internal and fork PRs.
- **A log is not evidence.** The summary says which test, which polluter and the
  witness command; the capsule is the artifact.

---

## The workflow

Two jobs: the existing test job saves its `-json` log even when it fails, and a
follow-up job turns that log into a comment. Drop it into the workflow that
already runs `go test`.

{% raw %}
```yaml
permissions:
  contents: read

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod

      - name: go test (keep the log even when red)
        # `tee` + `pipefail` keeps the failing exit code, so the job still goes red.
        run: |
          set -o pipefail
          go test -json ./... | tee "$RUNNER_TEMP/go-test.json"

      - name: save the -json log
        if: failure()
        uses: actions/upload-artifact@v7
        with:
          name: go-test-json
          path: ${{ runner.temp }}/go-test.json
          if-no-files-found: ignore

  repro:
    needs: test
    if: failure()
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod

      - uses: actions/download-artifact@v8
        with:
          name: go-test-json
          path: ${{ runner.temp }}

      - name: install go-flake-hunter
        run: GOBIN="$RUNNER_TEMP" go install github.com/emrelab/go-flake-hunter/cmd/go-flake-hunter@v0.3.0

      - name: shrink the failures to a witness
        id: repro
        # `--format markdown` is already a PR-comment body. Budget is deliberately
        # smaller than a full hunt: this job has suspects, not a search space.
        run: |
          set +e
          "$RUNNER_TEMP/go-flake-hunter" repro \
            --from-json "$RUNNER_TEMP/go-test.json" \
            --capsule-dir "$RUNNER_TEMP/capsules" \
            --format markdown --budget 200 --timeout 10m > "$RUNNER_TEMP/repro.md"
          echo "code=$?" >> "$GITHUB_OUTPUT"
          shopt -s nullglob
          capsules=("$RUNNER_TEMP"/capsules/gfh_*.json)
          if [ "${#capsules[@]}" -gt 0 ]; then echo "capsules=true" >> "$GITHUB_OUTPUT"; fi

      - name: add findings to the job summary
        if: steps.repro.outputs.code == '0'
        run: cat "$RUNNER_TEMP/repro.md" >> "$GITHUB_STEP_SUMMARY"

      - name: keep the capsules
        if: always() && steps.repro.outputs.capsules == 'true'
        uses: actions/upload-artifact@v7
        with:
          name: go-flake-hunter-capsules-${{ strategy.job-index || '0' }}-${{ github.run_attempt }}
          path: ${{ runner.temp }}/capsules/gfh_*.json
          if-no-files-found: error
```
{% endraw %}

## What was verified, and what was not

Verified locally on this repo (the command, its output shape and every exit code
below). The real output, indented because it contains a fence of its own:

    $ go-flake-hunter repro --from-json failure.json --format markdown --budget 400 --timeout 120s
    ### flake: TestVictim
    
    | field | value |
    |---|---|
    | package | fixture/orderbasic |
    | class | order |
    | polluter | TestPolluter |
    | run | 3/3 failed |
    | evidence | TestPolluter writes, TestVictim reads: seeded |
    | status | confirmed |
    | capsule | `gfh_ad2495a77e2c` |
    
    Reproduce:
    
    ```sh
    go test . -run '^TestFlakeWitness$' -count=1 -parallel=1
    ```
    
    Permanent regression test: `checkout_TestVictim_goflakehunter_test.go` (build tag `goflakehunter`).
    
- `--format markdown` is the comment body, byte for byte — no templating needed.
- `--from-json` resolves import paths with `go list`, so it must run inside the
  repository the log came from.
- Exit codes: `0` witness found, `3` not reproduced, `4` budget exhausted
  (`internal/cli/repro.go:77-83`).
The workflow uses `failure()` so the repro job runs after a failed test job.
The job summary uses the workflow’s read-only permissions, including on fork PRs.
