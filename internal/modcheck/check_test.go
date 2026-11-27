package modcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGenerateServicePassesStrictCheck(t *testing.T) {
	parent := t.TempDir()
	target, err := GenerateModule(ScaffoldOptions{
		ParentDir:  parent,
		Kind:       "service",
		Name:       "demo-svc",
		ModulePath: "example.invalid/demo-svc",
		GoVersion:  "1.26.0",
	})
	if err != nil {
		t.Fatalf("GenerateModule(service): %v", err)
	}
	res, err := Check(Options{
		Dir:       target,
		Level:     LevelStrict,
		AllowEnv:  []string{"LISTEN_ADDR"},
		WithTools: true,
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(res.Problems) > 0 {
		t.Fatalf("expected generated service to pass strict check, got:\n%s", strings.Join(res.Problems, "\n"))
	}
}

func TestGenerateLibPassesStrictWithToolsCheck(t *testing.T) {
	parent := t.TempDir()
	target, err := GenerateModule(ScaffoldOptions{
		ParentDir:  parent,
		Kind:       "lib",
		Name:       "retry-kit",
		ModulePath: "example.invalid/retrykit",
		GoVersion:  "1.26.0",
	})
	if err != nil {
		t.Fatalf("GenerateModule(lib): %v", err)
	}
	res, err := Check(Options{
		Dir:       target,
		Level:     LevelStrict,
		ForbidEnv: true,
		WithTools: true,
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(res.Problems) > 0 {
		t.Fatalf("expected generated lib to pass strict --forbid-env --with-tools check, got:\n%s", strings.Join(res.Problems, "\n"))
	}
}

func TestLevelsAndRules(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, repo, map[string]string{
		"host/go.mod": "module example.invalid/host\n\ngo 1.26.0\n",
		"mod/go.mod":  "module example.invalid/mod\n\ngo 1.26.0\n\nreplace example.invalid/other => ../other\n",
		"mod/internal/core/core.go": `package core
import (
	"os"
	_ "example.invalid/mod/domain/flow"
)
type Secret struct{}
func init() { _ = os.Getenv("LEAKED") }
`,
		"mod/domain/flow/flow.go": `package flow
import _ "example.invalid/host/biz"
func Run() {}
`,
		"mod/utils/utils.go": `package utils
import "example.invalid/mod/internal/core"
func Leak() core.Secret { return core.Secret{} }
`,
		"mod/cmd/app/main.go": `package main
import "os"
func main() { _ = os.Getenv("FORBIDDEN_KEY") }
`,
	})

	modDir := filepath.Join(repo, "mod")

	// Relaxed catches local replace and cross-module import, but NOT trash pkg or init side effects.
	relaxed, err := Check(Options{Dir: modDir, Level: LevelRelaxed, SkipStandalone: true})
	if err != nil {
		t.Fatalf("relaxed Check: %v", err)
	}
	joinedRelaxed := strings.Join(relaxed.Problems, "\n")
	for _, want := range []string{"[mod/local-replace]", "[import/cross-module]"} {
		if !strings.Contains(joinedRelaxed, want) {
			t.Errorf("relaxed missing %s in:\n%s", want, joinedRelaxed)
		}
	}
	if strings.Contains(joinedRelaxed, "[layout/no-trash-pkg]") {
		t.Errorf("relaxed should not report trash package:\n%s", joinedRelaxed)
	}

	// Standard adds trash package and init side effects; explicit --forbid-layer enforces layer isolation.
	std, err := Check(Options{
		Dir:            modDir,
		Level:          LevelStandard,
		ForbidLayers:   []Pair{{Left: "internal", Right: "domain"}},
		SkipStandalone: true,
	})
	if err != nil {
		t.Fatalf("standard Check: %v", err)
	}
	joinedStd := strings.Join(std.Problems, "\n")
	for _, want := range []string{
		"[layer/isolation]",
		"[layout/no-trash-pkg]",
		"[init/no-side-effects]",
	} {
		if !strings.Contains(joinedStd, want) {
			t.Errorf("standard missing %s in:\n%s", want, joinedStd)
		}
	}

	// Explicit --allow-env and --forbid-env work regardless of level.
	envAllow, err := Check(Options{
		Dir:            modDir,
		Level:          LevelRelaxed,
		AllowEnv:       []string{"PORT"},
		SkipStandalone: true,
	})
	if err != nil {
		t.Fatalf("allow-env Check: %v", err)
	}
	if !strings.Contains(strings.Join(envAllow.Problems, "\n"), "[env/allowlist]") {
		t.Errorf("expected [env/allowlist], got:\n%s", strings.Join(envAllow.Problems, "\n"))
	}

	envForbid, err := Check(Options{
		Dir:            modDir,
		Level:          LevelRelaxed,
		ForbidEnv:      true,
		SkipStandalone: true,
	})
	if err != nil {
		t.Fatalf("forbid-env Check: %v", err)
	}
	if !strings.Contains(strings.Join(envForbid.Problems, "\n"), "[env/forbidden]") {
		t.Errorf("expected [env/forbidden], got:\n%s", strings.Join(envForbid.Problems, "\n"))
	}
}

func TestPublicAPILeakAndAllowInternalIn(t *testing.T) {
	leakDir := t.TempDir()
	writeFiles(t, leakDir, map[string]string{
		"go.mod":                   "module example.invalid/leak\n\ngo 1.26.0\n",
		"internal/secret/types.go": "package secret\ntype Token struct{}\n",
		"pub.go":                   "package leak\nimport \"example.invalid/leak/internal/secret\"\nfunc Expose() secret.Token { return secret.Token{} }\n",
		"app/app.go":               "package app\nimport \"example.invalid/leak/internal/secret\"\nfunc Run() secret.Token { return secret.Token{} }\n",
	})
	res, err := Check(Options{Dir: leakDir, Level: LevelStandard, SkipStandalone: true})
	if err != nil {
		t.Fatalf("leak Check: %v", err)
	}
	joined := strings.Join(res.Problems, "\n")
	if !strings.Contains(joined, "pub.go:3 [api/no-internal-leak]") || !strings.Contains(joined, "app/app.go:3 [api/no-internal-leak]") {
		t.Fatalf("expected api/no-internal-leak on both pub.go and app/app.go, got:\n%s", joined)
	}

	// Exempting app/ via AllowInternalIn still catches root pub.go.
	res2, err := Check(Options{Dir: leakDir, Level: LevelStandard, AllowInternalIn: []string{"app"}, SkipStandalone: true})
	if err != nil {
		t.Fatalf("leak Check with AllowInternalIn: %v", err)
	}
	joined2 := strings.Join(res2.Problems, "\n")
	if strings.Contains(joined2, "app/app.go") {
		t.Errorf("expected app/ to be exempted, got:\n%s", joined2)
	}
	if !strings.Contains(joined2, "pub.go:3 [api/no-internal-leak]") {
		t.Errorf("expected root pub.go leak to remain reported, got:\n%s", joined2)
	}
}

func TestLayoutRules(t *testing.T) {
	layoutDir := t.TempDir()
	writeFiles(t, layoutDir, map[string]string{
		"go.mod":             "module example.invalid/layout\n\ngo 1.26.0\n",
		"pkg/foo/foo.go":     "package foo\n",
		"bad_dir/wrong.go":   "package bad_name\n",
		"mismatch/actual.go": "package other\n",
	})
	layoutRes, err := Check(Options{Dir: layoutDir, Level: LevelStandard, SkipStandalone: true})
	if err != nil {
		t.Fatalf("layout Check: %v", err)
	}
	joinedLayout := strings.Join(layoutRes.Problems, "\n")
	for _, want := range []string{"[layout/no-pkg-dir]", "[layout/pkg-naming]", "[layout/pkg-dir-match]"} {
		if !strings.Contains(joinedLayout, want) {
			t.Errorf("expected %s in:\n%s", want, joinedLayout)
		}
	}
}

func TestCustomRestrictions(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod": "module example.invalid/agent\n\ngo 1.26.0\n",
		"domain/scene/scene.go": `package scene
import _ "example.invalid/agent/internal/storage/pg"
type runner interface{ InvokableRun() }
func Call(r runner) { r.InvokableRun() }
`,
		"internal/storage/pg/pg.go": "package pg\n",
	})
	res, err := Check(Options{
		Dir:          dir,
		Level:        LevelStandard,
		ForbidLayers: []Pair{{Left: "domain", Right: "internal/storage"}},
		ForbidCalls:  []string{"InvokableRun"},
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	joined := strings.Join(res.Problems, "\n")
	for _, want := range []string{"[layer/isolation]", "[call/forbidden]"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s in:\n%s", want, joined)
		}
	}
}

func TestGCIToolSpec(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod": "module example.invalid/gcimod\n\ngo 1.26.0\n",
		"doc.go": "package gcimod\n",
	})
	fakeBin := filepath.Join(t.TempDir(), "fake-gci")
	script := `#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = "prefix(example.invalid/gcimod)" ]; then
    echo "doc.go"
    exit 0
  fi
done
echo "missing expected prefix arg: $*" >&2
exit 2
`
	if err := os.WriteFile(fakeBin, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	res, err := Check(Options{
		Dir:            dir,
		Level:          LevelStandard,
		GCISpec:        fakeBin,
		SkipStandalone: true,
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	joined := strings.Join(res.Problems, "\n")
	if !strings.Contains(joined, "[tools/gci]") || !strings.Contains(joined, "doc.go") {
		t.Fatalf("expected [tools/gci] reporting doc.go, got:\n%s", joined)
	}
}
