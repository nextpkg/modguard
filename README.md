# modguard

`modguard` is a **zero-YAML, composable CLI** for enforcing Go module boundaries, package architecture rules, out-of-repo standalone exportability, and SDK contracts — plus generating compliant standalone module and SDK scaffolds.

## Install

```sh
go install github.com/nextpkg/modguard/cmd/modguard@latest
```

Or run a pinned version directly without installing:

```sh
go run github.com/nextpkg/modguard/cmd/modguard@v0.1.0 check --level strict .
```

## Why `modguard`

Standard Go tools (`gofmt`, `go vet`, `staticcheck`, `deadcode`, `govulncheck`, `gosec`) check **file- and function-level** quality. `modguard` fills the **module- and architecture-level** gap with zero config files:

- **True module independence**: blocks local `replace` directives, relative imports, symlinks, and accidental cross-module imports inside monorepos; at `strict` level, copies the module into an out-of-repo temp directory and runs `GOWORK=off go test -mod=readonly -count=1 ./...`.
- **Package hygiene**: blocks redundant top-level `pkg/` directories, generic trash package names (`util`, `utils`, `common`, `helper`, `helpers`, `misc`, `shared`, `pkg`), non-idiomatic package names, package/directory mismatches, and `init()` side effects (`os.Getenv`, `net.Dial`, `http.Get`, `sql.Open`) in non-`main` packages.
- **API encapsulation**: uses `go/types` to catch exported types, functions, fields, generics, and interfaces leaking `internal/` types.
- **Composable CLI rules**: compose `--allow-import`, `--forbid-import`, `--only-import-in`, `--forbid-layer`, `--forbid-call`, `--allow-env`, `--forbid-env`, `--allow-internal-in`, and `--exclude-dir-test` directly in your `Makefile` or CI scripts — no YAML schema to maintain.
- **Optional tool orchestration**: run `gofmt` + `go vet` via `--with-tools`, run the full lint suite (`gofmt`, `go vet`, `gci`, `staticcheck`, `deadcode`) via `--all-tools`, or run security scans (`govulncheck`, `gosec`) via `--security`. Each tool can also be enabled or version-overridden individually (`--gci`, `--staticcheck`, `--deadcode`, `--govulncheck`, `--gosec`, accepting `auto`, a binary in `PATH`, or `pkg@version`).
- **SDK verification & scaffolding**: `modguard sdk check` validates multi- and single-provider Go SDK layouts, SSA supplier contract compliance, zero `internal/` re-exports, and out-of-repo instrumented external consumer tests.

## Commands

```text
modguard check [flags] [dir ...]          Check one or more standalone Go modules (default: .)
modguard new [flags]                      Scaffold a standalone Go service or library module
modguard sdk check [flags] [dir]          Check an SDK collection and standalone consumer tests (default: .)
modguard sdk new [flags] [dir]            Scaffold a multi- or single-provider Go SDK (default: .)
modguard skill install [flags] [dir]      Install modguard skill into a target repo (default: .)
modguard skill update [flags] [dir]       Update an existing modguard skill installation (default: .)
modguard skill remove [flags] [dir]       Remove modguard skill from a target repo (default: .)
modguard version                          Print modguard version
modguard help [command]                   Show help for modguard or a subcommand
```

### Strictness Levels (`modguard check --level`)

| Level | Built-in Rules |
| --- | --- |
| `relaxed` | No relative imports, no symlinks, no local `replace` in `go.mod`, no imports from sibling/host repo modules (unless in `--allow-import`) |
| `standard` *(default)* | `relaxed` + no top-level `pkg/`, no trash package names, idiomatic lowercase package names matching directory name, no `init()` side effects in non-`main` packages, no `internal/` leaks in exported APIs |
| `strict` | `standard` + out-of-repo temp-dir `GOWORK=off go test -mod=readonly -count=1 ./...` verification (skip with `--skip-standalone`) |

Explicit CLI flags (`--forbid-layer`, `--only-import-in`, `--forbid-call`, `--allow-env`, `--forbid-env`, etc.) are always enforced whenever passed, regardless of `--level`. All list/rule flags accept both repeated flags (`--allow-env A --allow-env B`) and comma-separated values (`--allow-env A,B`), and flags may appear before or after directory arguments.

## Examples

### Check standalone modules in a monorepo

```sh
# Check multiple modules with strict standalone export verification
modguard check --level strict ./gateway ./worker

# Enforce an exact environment variable allowlist
modguard check --level strict --allow-env PORT,LOG_LEVEL ./egress

# Pure library: forbid all direct env reads and run full lint + security suite
modguard check --level strict --forbid-env --all-tools --security .

# Enforce layered isolation and restricted third-party SDK usage
modguard check --level strict \
  --allow-import github.com/org/repo/sdks \
  --forbid-layer internal:app \
  --forbid-layer app:internal/storage \
  --only-import-in github.com/vendor/sdk/=internal/adapter \
  --allow-internal-in app \
  --exclude-dir-test app:./internal/... \
  ./service
```

### Scaffold a new module or SDK

```sh
# Standalone Go service (passes strict + --with-tools out of the box)
modguard new --kind service --dir ./services --name billing --module github.com/org/repo/services/billing

# Standalone Go library / framework
modguard new --kind lib --dir . --name retrykit --module github.com/org/retrykit

# Multi-provider or single-platform SDK inside an SDK collection module
modguard sdk new --kind multi --name ocr --op Recognize --provider aliyun ./sdks
modguard sdk new --kind single --name oauth --op Exchange ./sdks

# Validate all SDKs including out-of-repo consumer coverage tests
modguard sdk check ./sdks
```

## Bundled Agent Skill

A reusable AI agent skill covering Go module architecture, SDK design boundaries, and semantic review checklists is embedded directly in the `modguard` binary and maintained under [`skill/`](skill/SKILL.md).

Install, update, or remove it in any repository:

```sh
# Install into ./.agents/skills/modguard (or specify another repo path)
modguard skill install /path/to/repo

# Custom skills directory (e.g. .claude/skills or an absolute path)
modguard skill install --dest .claude/skills /path/to/repo

# Update an existing installation (replaces files cleanly and removes stale ones)
modguard skill update /path/to/repo

# Remove an installed modguard skill (verifies SKILL.md ownership before deleting)
modguard skill remove /path/to/repo
```

## License

MIT — see [LICENSE](LICENSE).
