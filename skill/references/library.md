# Building and Reviewing Pure Go Libraries & Protocol Packages

Use this guide for **stateless or in-process Go libraries**: protocol codecs, wire types, stream state machines, parsers, algorithms, middleware, and shared contracts.

> **Do NOT put pure libraries under a `modguard sdk check` directory**, and **never fabricate a dummy `Config` or `Client`** just to satisfy SDK layout rules. Reserve `modguard sdk check` for external supplier/platform client SDKs (see [sdk.md](sdk.md)).

## Scaffolding and Verification

```sh
# Scaffold a standalone library (generates go.mod, Makefile, README.md, root package, black-box test)
modguard new --kind lib --dir <parent-dir> --name <lib-name> --module <go-module-path>

# Verify boundaries, zero ambient env reads, formatting/vet, and out-of-repo GOWORK=off exportability
modguard check --level strict --forbid-env --with-tools <lib-dir>
```

## Layout & Design Rules

- **No forced file template**: organize files by responsibility (`types.go`, `codec.go`, `stream.go`, `errors.go`, etc.). Keep internal helpers under `internal/`.
- **True module independence**: own `go.mod`, no local `replace` pointing back into the host repo, no imports from host services (`gateway`, `business`, `agent`, etc.), and must pass `GOWORK=off go test -mod=readonly ./...` outside the repo.
- **Zero ambient side effects**: no `os.Getenv` / network / DB calls in `init()`, package-level vars, or library functions (`--forbid-env`). Pass limits, timeouts, or options explicitly via parameters or an `Options` struct with sensible defaults.
- **Explicit error contract**: export sentinel errors (`var Err... = errors.New(...)`) or typed errors in `errors.go` so callers can match with `errors.Is` / `errors.As`. Keep machine error codes distinct from human-readable messages.
- **Host-agnostic API & tags**: never leak host-specific struct tags (e.g. internal codegen tags), product domain jargon, or hardcoded host business thresholds into a general-purpose library or protocol parser.
- **Clean package naming**: lowercase ASCII matching the directory name; never use generic trash names (`util`, `utils`, `common`, `helper`, `shared`, `misc`, `pkg`).

## Avoiding Silent Drift (CI & Repo Wiring)

Creating a standalone library only protects the codebase if checks actually run:

1. **Module-level `Makefile`**: every new library keeps its own `check` / `verify` target running `modguard check --level strict --forbid-env .`.
2. **Repo-level gate**: wire every new library module into the repository's top-level `make verify` / CI workflow (or auto-discover modules from `go.work` / a designated directory such as `libs/*` or `protocol/*`) so new libraries cannot bypass checks.
3. **Real production adoption**: do not leave an unreferenced library half-migrated in the repo; wire callers or keep it behind an active migration PR that passes deadcode checks.
