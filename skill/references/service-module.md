# Standalone Go Module Layout & Architecture Guide

Principle: **enforce structural and dependency rules automatically with `modguard check`; review naming, domain decomposition, and abstraction boundaries deliberately.**

## Four Standard Go Module Paradigms

| Module Kind | Recommended Layout | Guard Command | Typical Use |
| --- | --- | --- | --- |
| **Pure Library / Protocol Package** | Root package organized by responsibility (`types.go`, `codec.go`, `errors.go`) + optional `internal/` + black-box `*_test.go` (no forced `Client`/`Config`) | `modguard check --level strict --forbid-env` | Protocol codecs, state machines, parsers, shared contracts |
| **External Client SDK** | Root package (`Config`, `types.go`, `errors.go`, `Client` or `Provider`) + `provider/` + `internal/providers/*` + `testdata/consumer_test.go` | `modguard sdk check <sdks-dir>` | Third-party / platform API clients (OCR, OAuth, OSS, LLM) |
| **Focused Service / CLI** | `cmd/<name>/main.go` (single place reading env & wiring) + `internal/` + optional root package (only when embeddable) + `go.mod` / `Makefile` / `README.md` | `modguard check --level strict --allow-env ...` | Gateways, workers, proxies, CLI tools |
| **Domain-Rich Service** | `cmd/<app>/main.go` + `internal/<domain>/` (split by business domain; within a domain use `http`/`service`/`repository` only when complexity warrants it) + `internal/platform/` (infrastructure; never imports domain) | `modguard check --level strict --forbid-layer ...` | Product APIs, workflow backends |

## Design Rules Beyond Static Analysis

- **Slice by domain, not giant flat layers**: avoid dumping dozens of unrelated features into one giant `services/` or `models/` package. Group by domain first, and add handler/service/repository subpackages only where they carry real boundaries.
- **Clean module & repo naming**: short, lowercase, no underscores, no redundant `go-` prefix or `-go` suffix.
- **No stuttering**: do not repeat the package name in exported symbols (`gateway.New`, not `gateway.NewGateway`).
- **Avoid stdlib collisions**: when wrapping or extending a stdlib concept, pick a clear distinct name (`httpx`, `ximage`).
- **Never name directories or files after ticket numbers or dates**: name by responsibility and domain.

## Rules Enforced Automatically by `modguard check`

- **True independence**: forbids local `replace`, relative imports, symlinks, and sibling/host module imports; `strict` copies the module outside the repo and runs `GOWORK=off go test -mod=readonly -count=1 ./...`.
- **Package hygiene**: forbids top-level `pkg/` directory, generic trash package names (`util`/`utils`/`common`/`helper`/`helpers`/`misc`/`shared`/`pkg`), non-lowercase/underscored package names, and package/directory mismatches.
- **Encapsulation & side effects**: catches `internal/` type leaks in exported APIs via `go/types`, blocks `init()` env/network/DB side effects in non-`main` packages, and enforces composable CLI rules (`--forbid-layer`, `--only-import-in`, `--forbid-call`, `--allow-env`, `--forbid-env`, `--exclude-dir-test`).
