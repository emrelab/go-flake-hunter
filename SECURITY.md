# Security Policy

## Supported versions

| Version | Supported |
|---------|-----------|
| 0.3.x   | ✅        |
| ≤ 0.2.x | ❌        |

## Reporting a vulnerability

Use GitHub's [private vulnerability
reporting](https://github.com/emrelab/go-flake-hunter/security/advisories/new)
("Report a vulnerability" under the Security tab). Please do not open a public
issue; a fix and advisory ship together.

## Threat model — what this tool actually does

Honest scope, so you can judge the risk yourself:

- `go-flake-hunter hunt` **executes `go test`** in the packages you point it at.
  It runs your own code, with your own permissions — nothing more.
- During a scan it **writes temporary witness files**
  (`zz_goflakehunter_witness_*_test.go`) into the scanned packages and deletes
  them after the run, including on SIGINT. `compile` writes one build-tagged
  regression test into the package you ask for.
- **Zero external dependencies, no network code of its own**: the binary
  shells out to the `go` tool and reads files. Anything that touches the
  network during a scan does so because *your* tests do.
- Capsules (`.go-flake-hunter/*.json`) record test names, file paths, ports and
  environment **key names — never values**. Treat a capsule like a build log:
  shareable, but check it before posting paths you consider sensitive.


## Rollback

Pin the Action or install command to the previous supported release while a
fixed patch is prepared. Keep the affected tag and its assets intact for
investigation. Verify the rollback binary with the release checksums and, when
available, `gh attestation verify <binary> --repo emrelab/go-flake-hunter`.
