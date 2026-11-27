---
name: modguard
description: Standalone Go module and SDK architecture discipline using github.com/nextpkg/modguard. Use when creating or auditing standalone Go services, reusable libraries, or Go SDK collections, enforcing dependency direction/env allowlists/out-of-repo standalone exportability, or scaffolding new modules.
---

# modguard — Standalone Go Module & SDK Discipline

`modguard` (`github.com/nextpkg/modguard`) enforces **package-, module-, and architecture-level boundaries** without YAML config, complementing `gofmt`, `go vet`, `staticcheck`, `deadcode`, `govulncheck`, and `gosec`.

## Reference Routing

Choose the right category before creating files — **do not force pure libraries into an `sdk check` directory**:

- **External supplier / platform client SDKs** (calls an external API/service; needs `Config` + `Client` or `provider/` + `internal/providers/*` + external consumer tests) → read [references/sdk.md](references/sdk.md) and run `modguard sdk check`
- **Pure libraries & protocol packages** (codecs, wire types, state machines, parsers, shared contracts — no external client/IO; never fabricate a dummy `Client`/`Config`) → read [references/library.md](references/library.md) and run `modguard check --level strict --forbid-env`
- **Standalone Go services & CLIs** (`cmd/<name>/main.go`, explicit env allowlists, layer direction rules) → read [references/service-module.md](references/service-module.md) and run `modguard check --level strict`

## Quick Commands

```sh
# Install globally
go install github.com/nextpkg/modguard/cmd/modguard@latest

# Install, update, or remove this skill in any repository (default: ./.agents/skills/modguard)
modguard skill install /path/to/repo
modguard skill update /path/to/repo
modguard skill remove /path/to/repo

# Check one or more standalone modules (strict includes out-of-repo GOWORK=off export test)
modguard check --level strict ./service-a ./service-b

# Check a pure library with zero direct env reads + formatting/vet
modguard check --level strict --forbid-env --with-tools .

# Scaffold a compliant standalone service or library
modguard new --kind service --dir ./services --name billing --module github.com/org/repo/services/billing
modguard new --kind lib --dir . --name retrykit --module github.com/org/retrykit

# Scaffold and verify Go SDKs
modguard sdk new --kind multi --name ocr --op Recognize --provider example ./sdks
modguard sdk new --kind single --name oauth --op Exchange ./sdks
modguard sdk check ./sdks
```

## Strictness Levels (`--level`)

| Level | Built-in Rules |
| --- | --- |
| `relaxed` | No relative imports, no symlinks, no local `replace` in `go.mod`, no imports from sibling/host repo modules (unless allowed via `--allow-import`) |
| `standard` *(default)* | `relaxed` + no top-level `pkg/`, no generic trash package names (`util`/`common`/`helper`/`misc`/`shared`/`pkg`), lowercase ASCII package names matching directory name, no `init()` side effects in non-`main` packages, no `internal/` leaks in exported APIs |
| `strict` | `standard` + out-of-repo temp-directory `GOWORK=off go test -mod=readonly -count=1 ./...` verification (including `--exclude-dir-test`) |

## Semantic Red Lines (Human & Agent Review)

Static analysis cannot judge domain semantics. Always review these explicitly:

- **Domain-neutral naming in shared modules**: package names, exported types, config keys, sentinel errors, and log fields in libraries, gateways, and SDKs express generic capability or protocol semantics — never host product jargon, workflow assumptions, or ticket IDs.
- **Mechanism vs. policy separation**: shared modules own reusable mechanisms (transport, auth, rate limiting, storage, protocol adaptation); host applications own business policies (state transitions, retries/refunds, prompts, copy, permissions) injected via small interfaces or explicit config.
- **Small interfaces & no pass-through bloat**: accept small interfaces, return concrete types; do not invent single-implementation mega-interfaces or empty pass-through layers solely for symmetry.
- **Error & logging boundaries**: never leak raw SQL, vendor payloads, or secrets into external error messages; wrap errors with `%w` and log once at the handling boundary.
- **Behavior-driven tests**: test observable contracts and failure modes (timeouts, cancellation, invalid input, upstream errors); do not test mocks against themselves or treat offline fakes as live integration verification.
- **Wire every new module into CI**: every new service, library, or SDK collection must expose a module-level `check`/`verify` target and be wired into (or auto-discovered by) the repository's top-level `verify` / CI gate so newly added modules are never silently skipped.

## Skill Self-Maintenance & Tool Feedback

- **Keep the installed skill in sync**: this skill is embedded inside the `modguard` binary. After upgrading `modguard`, run `modguard skill update .` (or pass `--dest <dir>` if installed to a custom directory) to atomically refresh files and prune stale ones. Avoid hand-editing the installed copy directly, as updates overwrite managed files. To uninstall cleanly, run `modguard skill remove .`.
- **Report tool defects upstream**: if `modguard` produces a false positive, misses a generic Go boundary issue, or exhibits a bug, proactively raise the issue with a minimal reproducer so `github.com/nextpkg/modguard` can be fixed — do not silently weaken `--level`, drop checks, or add workarounds in product code.
