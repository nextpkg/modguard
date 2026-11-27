package modcheck

import (
	"fmt"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var validModuleDir = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// ScaffoldOptions defines parameters for generating a compliant standalone Go module.
type ScaffoldOptions struct {
	ParentDir  string
	Kind       string // "service" (default) or "lib"
	Name       string
	ModulePath string
	GoVersion  string
}

// GenerateModule creates a minimal standalone Go module ("service" or "lib")
// that passes `modguard check --level strict --with-tools` out of the box.
func GenerateModule(opts ScaffoldOptions) (string, error) {
	kind := strings.ToLower(strings.TrimSpace(opts.Kind))
	if kind == "" {
		kind = "service"
	}
	if kind != "service" && kind != "lib" {
		return "", fmt.Errorf("unknown module kind %q (use service or lib)", opts.Kind)
	}
	name := strings.TrimSpace(opts.Name)
	if !validModuleDir.MatchString(name) {
		return "", fmt.Errorf("invalid module name %q (use lowercase letters, digits, hyphens)", name)
	}
	pkgName := strings.ReplaceAll(name, "-", "")
	if !token.IsIdentifier(pkgName) || token.Lookup(pkgName).IsKeyword() || types.Universe.Lookup(pkgName) != nil || trashPackageNames[pkgName] {
		return "", fmt.Errorf("invalid Go package name %q derived from %q", pkgName, name)
	}
	modPath := strings.TrimSpace(opts.ModulePath)
	if modPath == "" {
		return "", fmt.Errorf("missing --module (e.g. github.com/org/%s)", name)
	}
	goVer := strings.TrimSpace(opts.GoVersion)
	if goVer == "" {
		goVer = "1.26.0"
	}
	parent, err := filepath.Abs(opts.ParentDir)
	if err != nil {
		return "", err
	}
	target := filepath.Join(parent, name)
	if _, err := os.Lstat(target); err == nil {
		return "", fmt.Errorf("destination already exists: %s", target)
	} else if !os.IsNotExist(err) {
		return "", err
	}

	var files map[string]string
	if kind == "lib" {
		files = libFiles(name, pkgName, modPath, goVer)
	} else {
		files = serviceFiles(name, pkgName, modPath, goVer)
	}

	staging, err := os.MkdirTemp(parent, ".modguard-new-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)
	tree := filepath.Join(staging, name)
	for rel, content := range files {
		full := filepath.Join(tree, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			return "", err
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			return "", err
		}
	}
	if err := os.Rename(tree, target); err != nil {
		return "", err
	}
	return target, nil
}

func commonMakefile() string {
	return `.PHONY: build test verify

build:
	GOWORK=off go build ./...

test:
	GOWORK=off go test -count=1 ./...

verify:
	GOWORK=off go vet ./...
	$(MAKE) test
`
}

func libFiles(name, pkgName, modPath, goVer string) map[string]string {
	return map[string]string{
		"go.mod":   fmt.Sprintf("module %s\n\ngo %s\n", modPath, goVer),
		"Makefile": commonMakefile(),
		"README.md": fmt.Sprintf(`# %s

Standalone Go library module `+"`%s`"+`.

## Layout

- Root package (`+"`%s`"+`): public interfaces, functional options, sentinel errors, and constructors.
- `+"`internal/engine/`"+`: private implementation isolated from external callers.
- `+"`%s_test.go`"+`: black-box (`+"`package %s_test`"+`) acceptance tests.

## Verify

`+"```sh"+`
GOWORK=off make verify
`+"```"+`
`, name, modPath, pkgName, pkgName, pkgName),
		"doc.go": fmt.Sprintf(`// Package %s provides a standalone, composable Go library.
//
// Public contracts, options, and sentinel errors live in the root package;
// implementation details are encapsulated under internal/.
package %s
`, pkgName, pkgName),
		"errors.go": fmt.Sprintf(`package %s

import "errors"

// ErrInvalidConfig is returned when required configuration is missing or invalid.
var ErrInvalidConfig = errors.New("%s: invalid configuration")
`, pkgName, name),
		"options.go": fmt.Sprintf(`package %s

// Config holds explicit settings for constructing a Client.
type Config struct {
	Name string
}

// Option mutates a Config during construction.
type Option func(*Config)

// WithName sets the instance name.
func WithName(name string) Option {
	return func(c *Config) {
		c.Name = name
	}
}
`, pkgName),
		pkgName + ".go": fmt.Sprintf(`package %s

import (
	"context"

	"%s/internal/engine"
)

// Runner defines the core capability exposed by this library.
type Runner interface {
	Run(ctx context.Context, input string) (string, error)
}

// Client is the default implementation of Runner.
type Client struct {
	eng *engine.Engine
}

// New constructs a Client using explicit functional options.
func New(opts ...Option) (*Client, error) {
	cfg := Config{Name: %q}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.Name == "" {
		return nil, ErrInvalidConfig
	}
	return &Client{eng: engine.New(cfg.Name)}, nil
}

// Run executes the library operation while honoring context cancellation.
func (c *Client) Run(ctx context.Context, input string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return c.eng.Execute(input), nil
}
`, pkgName, modPath, name),
		filepath.Join("internal", "engine", "engine.go"): `package engine

// Engine encapsulates private implementation state away from public callers.
type Engine struct {
	name string
}

// New creates a private Engine instance.
func New(name string) *Engine {
	return &Engine{name: name}
}

// Execute processes an input payload.
func (e *Engine) Execute(input string) string {
	if input == "" {
		return e.name
	}
	return e.name + ":" + input
}
`,
		pkgName + "_test.go": fmt.Sprintf(`package %s_test

import (
	"context"
	"errors"
	"testing"

	%s %q
)

func TestClientRun(t *testing.T) {
	if _, err := %s.New(%s.WithName("")); !errors.Is(err, %s.ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %%v", err)
	}
	var r %s.Runner
	client, err := %s.New(%s.WithName("demo"))
	if err != nil {
		t.Fatalf("New: %%v", err)
	}
	r = client
	got, err := r.Run(context.Background(), "ping")
	if err != nil {
		t.Fatalf("Run: %%v", err)
	}
	if got != "demo:ping" {
		t.Fatalf("expected demo:ping, got %%q", got)
	}
}
`, pkgName, pkgName, modPath, pkgName, pkgName, pkgName, pkgName, pkgName, pkgName),
	}
}

func serviceFiles(name, pkgName, modPath, goVer string) map[string]string {
	return map[string]string{
		"go.mod":   fmt.Sprintf("module %s\n\ngo %s\n", modPath, goVer),
		"Makefile": commonMakefile(),
		"README.md": fmt.Sprintf(`# %s

Standalone Go service module `+"`%s`"+`.

## Build and verify

`+"```sh"+`
GOWORK=off make verify
`+"```"+`
`, name, modPath),
		"errors.go": fmt.Sprintf(`package %s

import "errors"

// ErrInvalidConfig is returned when required configuration is missing.
var ErrInvalidConfig = errors.New("%s: invalid configuration")
`, pkgName, name),
		"service.go": fmt.Sprintf(`package %s

import "context"

// Config holds explicit runtime dependencies for the service.
type Config struct {
	ListenAddr string
}

// Service implements the module's public entrypoint.
type Service struct {
	cfg Config
}

// New constructs a Service from explicit configuration.
func New(cfg Config) (*Service, error) {
	if cfg.ListenAddr == "" {
		return nil, ErrInvalidConfig
	}
	return &Service{cfg: cfg}, nil
}

// Ready reports whether the service is ready to serve requests.
func (s *Service) Ready(ctx context.Context) error {
	return ctx.Err()
}
`, pkgName),
		"service_test.go": fmt.Sprintf(`package %s_test

import (
	"context"
	"errors"
	"testing"

	%s %q
)

func TestServiceLifecycle(t *testing.T) {
	if _, err := %s.New(%s.Config{}); !errors.Is(err, %s.ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %%v", err)
	}
	svc, err := %s.New(%s.Config{ListenAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("New: %%v", err)
	}
	if err := svc.Ready(context.Background()); err != nil {
		t.Fatalf("Ready: %%v", err)
	}
}
`, pkgName, pkgName, modPath, pkgName, pkgName, pkgName, pkgName, pkgName),
		filepath.Join("cmd", name, "main.go"): fmt.Sprintf(`package main

import (
	"context"
	"log"
	"os"

	%s %q
)

func main() {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	svc, err := %s.New(%s.Config{ListenAddr: addr})
	if err != nil {
		log.Fatal(err)
	}
	if err := svc.Ready(context.Background()); err != nil {
		log.Fatal(err)
	}
}
`, pkgName, modPath, pkgName, pkgName),
	}
}
