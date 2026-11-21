---
layout: default
title: go-flake-hunter
description: Reproduce, shrink and permanently capture flaky Go tests.
---

Reproduce a flaky Go test in 30 seconds instead of 30 minutes, shrink it to the
smallest polluter→victim witness, and leave a permanent regression test behind.

```sh
go install github.com/emrelab/go-flake-hunter/cmd/go-flake-hunter@v0.4.0
```

| Where to go | What is there |
|---|---|
| [GitHub repository](https://github.com/emrelab/go-flake-hunter) | install, the first 5 minutes, every command and exit code |
| [Marketplace listing](https://github.com/marketplace/actions/go-flake-hunter) | the GitHub Action, same name as the repository |
| [Architecture](ARCHITECTURE.md) | package boundaries, algorithms, Go-specific traps |
| [CI reproduction recipe](CI-REPRO.md) | turn a red `go test -json` log into a PR comment |
| [Real-world evidence](real-world/README.md) | eight upstream flake cases, witness before and after |
| [Writing](writing/shrinking-1873-suspects-to-one-polluter.md) | how a 1736-test package's order dependence was shrunk to one polluter |

Apache-2.0. Not affiliated with any hosted vendor.
