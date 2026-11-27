# Building and Reviewing Go SDKs with `modguard`

When creating or modifying an SDK collection, verify with `modguard sdk check <sdks-dir>`.

## Scaffolding a New SDK

Use `multi` when a capability may be served by multiple suppliers; use `single` when a capability is bound to one platform. SDK and supplier names must be lowercase Go package names:

```sh
modguard sdk new --kind multi --name ocr --op Recognize --provider example ./sdks
modguard sdk new --kind single --name oauth --op Exchange ./sdks
```

The generator creates `config.go`, `types.go`, `errors.go`, the public entrypoint (`provider.go` + `provider/` + `internal/providers/<supplier>/` for `multi`, or `client.go` for `single`), and `testdata/consumer_test.go`. Single-platform SDKs that only expose `Config`, `Client`, and errors may omit `types.go`.

## Boundaries

- **Multi-provider**: root package defines the capability interface and neutral input/output types; `provider/` constructs and returns the capability interface from `Config`; supplier wire protocols, signing, and response mapping live under `internal/providers/<supplier>/`.
- **Single-platform**: root package exposes `New(Config)` and `Client` methods.
- **Config & dependencies**: define explicit `Config` structs and accept transport/dependencies explicitly (no ambient env reads). Import only the Go stdlib, packages within the same SDK, and independent third-party modules — never host or sibling repository packages.
- **Public API encapsulation**: keep internal types and helpers under `internal/`; exported functions, types, vars, and structs must not expose or re-export `internal/` symbols.

## Acceptance Checklist

- [ ] Core happy path and handleable failure modes (timeout, cancellation, upstream errors) are covered by tests.
- [ ] Exported names, parameters, errors, and implementations contain zero host-product domain assumptions.
- [ ] `testdata/consumer_test.go` invokes the public capability from an external module perspective (using local test servers / fixtures) and asserts results.
- [ ] `modguard sdk check <sdks-dir>` passes (verifies layout, API leaks, SSA supplier contract compliance, and out-of-repo instrumented external consumer coverage).
