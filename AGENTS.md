# Repository guide

## Purpose and design constraints

`github.com/nextpkg/modguard` is a standalone, general-purpose Go CLI for enforcing Go module boundaries, architectural rules, standalone exportability, and SDK contracts.

- **Zero YAML configuration**: all rules and options are expressed via composable CLI flags (repeatable and comma-separated). Do not add config-file loaders (`.modguard.yaml`, TOML, JSON config, etc.).
- **Zero project-specific backdoors**: never hardcode repository names, business directory names (such as `business/`), or legacy tool names (`sdkdev`) into built-in rules. Project-specific layout exceptions belong in caller-supplied CLI flags (`--forbid-layer`, `--allow-internal-in`, `--allow-import`, etc.).
- **Minimal runtime dependencies**: keep `go.mod` limited to the Go standard library plus `golang.org/x/mod` and `golang.org/x/tools`. Do not pull in heavy CLI frameworks or assertion libraries unless strictly justified.
- **Go version floor**: keep `go 1.26.0` compatibility in `go.mod` and scaffolds.

## Layout

- `cmd/modguard/`: CLI entry point (`check`, `new`, `sdk check`, `sdk new`, `version`, `help`), interleaved flag parsing, composable `stringList` flags, and CLI tests (`main_test.go`).
- `internal/modcheck/`: standalone module checker (`check.go`), `service`/`lib` scaffold generator (`scaffold.go`), and tests (`check_test.go`).
- `internal/sdk/`: SDK collection layout validator (`sdk.go`, `layout.go`), `go/types` API leak and binding re-export checker (`api.go`), SSA supplier contract checker (`supplier.go`), out-of-repo standalone + instrumented consumer coverage verifier (`standalone.go`, `coverage.go`), and SDK scaffolder (`generate.go`, `templates/`).
- `skill/`: generic reusable agent skill (`SKILL.md` + `references/`) for consumers who adopt `modguard`.

## Development and validation

Run all checks with `GOWORK=off` via GNU Make at the repository root:

```sh
make check          # gofmt check, go vet, go test -race, and self-dogfooding strict check
LINT=1 make check   # also runs staticcheck (-checks=all) and deadcode
make security       # runs govulncheck
make build          # builds ./bin/modguard
```

Before committing any change:

1. Run `make check` and `LINT=1 make check`.
2. Ensure new CLI flags or rule changes include focused tests in `cmd/modguard/main_test.go`, `internal/modcheck/check_test.go`, or `internal/sdk/*_test.go`.
3. Keep `README.md`, CLI help text, and `skill/` in sync with actual subcommands and flags.
4. Do not weaken checks or tests to hide failures.
