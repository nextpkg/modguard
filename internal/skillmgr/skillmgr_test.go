package skillmgr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nextpkg/modguard"
)

func TestInstallUpdateRemoveLifecycle(t *testing.T) {
	repo := t.TempDir()

	// Install into default .agents/skills/modguard.
	installed, err := Install(modguard.SkillFS, Options{TargetDir: repo})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	want := filepath.Join(repo, ".agents", "skills", "modguard")
	if installed != want {
		t.Fatalf("installed=%q want %q", installed, want)
	}
	if _, err := os.Stat(filepath.Join(want, "SKILL.md")); err != nil {
		t.Fatalf("missing SKILL.md: %v", err)
	}
	if _, err := os.Stat(filepath.Join(want, "references", "sdk.md")); err != nil {
		t.Fatalf("missing references/sdk.md: %v", err)
	}

	// Second Install without --force fails.
	if _, err := Install(modguard.SkillFS, Options{TargetDir: repo}); err == nil {
		t.Fatal("expected duplicate Install to fail without Force")
	}

	// Add a stale file, then Update removes stale files and restores embedded content.
	stale := filepath.Join(want, "stale.md")
	if err := os.WriteFile(stale, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(modguard.SkillFS, Options{TargetDir: repo}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("expected stale file to be removed on Update, err=%v", err)
	}

	// Remove deletes the installed skill cleanly.
	removed, err := Remove(Options{TargetDir: repo})
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if removed != want {
		t.Fatalf("removed=%q want %q", removed, want)
	}
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Fatalf("expected skill dir to be removed, err=%v", err)
	}

	// Update or Remove on missing dir fails cleanly.
	if _, err := Update(modguard.SkillFS, Options{TargetDir: repo}); err == nil {
		t.Fatal("expected Update on missing skill to fail")
	}
	if _, err := Remove(Options{TargetDir: repo}); err == nil {
		t.Fatal("expected Remove on missing skill to fail")
	}
}

func TestCustomDestAndRefuseUnrelatedDir(t *testing.T) {
	repo := t.TempDir()
	customDest := ".claude/skills"
	target := filepath.Join(repo, ".claude", "skills", "modguard")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("---\nname: other-skill\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Refuse to overwrite or delete an unrelated skill.
	if _, err := Update(modguard.SkillFS, Options{TargetDir: repo, DestDir: customDest}); err == nil || !strings.Contains(err.Error(), "not a modguard skill") {
		t.Fatalf("expected refusal on unrelated skill Update, got: %v", err)
	}
	if _, err := Remove(Options{TargetDir: repo, DestDir: customDest}); err == nil || !strings.Contains(err.Error(), "not a modguard skill") {
		t.Fatalf("expected refusal on unrelated skill Remove, got: %v", err)
	}
}
