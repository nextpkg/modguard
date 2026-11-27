package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIVersionAndHelp(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := run([]string{"version"}, &out, &errBuf); code != 0 {
		t.Fatalf("version exit=%d stderr=%s", code, errBuf.String())
	}
	if !strings.HasPrefix(out.String(), "modguard ") {
		t.Fatalf("unexpected version output: %q", out.String())
	}

	for _, topic := range [][]string{{"help"}, {"help", "check"}, {"help", "new"}, {"help", "sdk"}, {"help", "sdk", "check"}, {"check", "-h"}} {
		out.Reset()
		errBuf.Reset()
		if code := run(topic, &out, &errBuf); code != 0 {
			t.Fatalf("help %v exit=%d stderr=%s", topic, code, errBuf.String())
		}
	}

	out.Reset()
	errBuf.Reset()
	if code := run([]string{"nonexistent"}, &out, &errBuf); code != 2 {
		t.Fatalf("unknown command exit=%d, want 2", code)
	}
	out.Reset()
	errBuf.Reset()
	if code := run([]string{"check", "--bogus-flag"}, &out, &errBuf); code != 2 {
		t.Fatalf("unknown flag exit=%d, want 2", code)
	}
	out.Reset()
	errBuf.Reset()
	if code := run([]string{"help", "sdk", "bogus"}, &out, &errBuf); code != 2 {
		t.Fatalf("help sdk bogus exit=%d, want 2", code)
	}
}

func TestCLINewAndMultiDirInterleavedCheck(t *testing.T) {
	tmp := t.TempDir()
	var out, errBuf bytes.Buffer

	// Scaffold a service and a library.
	if code := run([]string{"new", "--kind", "service", "--dir", tmp, "--name", "alpha", "--module", "example.invalid/alpha"}, &out, &errBuf); code != 0 {
		t.Fatalf("new service exit=%d stderr=%s", code, errBuf.String())
	}
	if code := run([]string{"new", "--kind", "lib", "--dir", tmp, "--name", "beta", "--module", "example.invalid/beta"}, &out, &errBuf); code != 0 {
		t.Fatalf("new lib exit=%d stderr=%s", code, errBuf.String())
	}

	// Interleaved flags + multi-dir + repeated --allow-env flags.
	out.Reset()
	errBuf.Reset()
	alphaDir := filepath.Join(tmp, "alpha")
	betaDir := filepath.Join(tmp, "beta")
	code := run([]string{
		"check",
		alphaDir, betaDir,
		"--level", "strict",
		"--allow-env", "LISTEN_ADDR",
		"--allow-env", "EXTRA_KEY",
		"--with-tools",
	}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("check exit=%d stdout=%s stderr=%s", code, out.String(), errBuf.String())
	}
	if !strings.Contains(out.String(), "example.invalid/alpha (strict) passed") || !strings.Contains(out.String(), "example.invalid/beta (strict) passed") {
		t.Fatalf("unexpected stdout:\n%s", out.String())
	}
}

func TestCLISDKNewAndCheck(t *testing.T) {
	tmp := t.TempDir()
	sdksDir := filepath.Join(tmp, "sdks")
	if err := os.MkdirAll(sdksDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sdksDir, "go.mod"), []byte("module example.invalid/sdks\n\ngo 1.26.0\n"), 0644); err != nil {
		t.Fatal(err)
	}

	var out, errBuf bytes.Buffer
	code := run([]string{"sdk", "new", "--kind", "single", "--name", "ping", "--op", "Send", sdksDir}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("sdk new exit=%d stderr=%s", code, errBuf.String())
	}

	out.Reset()
	errBuf.Reset()
	code = run([]string{"sdk", "check", "--skip-standalone", sdksDir}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("sdk check exit=%d stderr=%s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "1 SDK(s) passed") {
		t.Fatalf("unexpected sdk check stdout: %q", out.String())
	}
}

func TestCLISkillLifecycle(t *testing.T) {
	repo := t.TempDir()
	var out, errBuf bytes.Buffer

	if code := run([]string{"skill", "install", repo}, &out, &errBuf); code != 0 {
		t.Fatalf("skill install exit=%d stderr=%s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "installed modguard skill") {
		t.Fatalf("unexpected install stdout: %q", out.String())
	}

	out.Reset()
	errBuf.Reset()
	if code := run([]string{"skill", "update", repo}, &out, &errBuf); code != 0 {
		t.Fatalf("skill update exit=%d stderr=%s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "updated modguard skill") {
		t.Fatalf("unexpected update stdout: %q", out.String())
	}

	out.Reset()
	errBuf.Reset()
	if code := run([]string{"skill", "remove", repo}, &out, &errBuf); code != 0 {
		t.Fatalf("skill remove exit=%d stderr=%s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "removed modguard skill") {
		t.Fatalf("unexpected remove stdout: %q", out.String())
	}
}
