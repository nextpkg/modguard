// Package skillmgr installs, updates, and removes the embedded modguard agent
// skill in target repositories or user skill directories.
package skillmgr

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DefaultSkillsDir is the default relative directory for repository agent skills.
	DefaultSkillsDir = ".agents/skills"
	// SkillName is the directory name of the installed modguard skill.
	SkillName = "modguard"
)

// Options controls where and how the modguard skill is managed.
type Options struct {
	TargetDir string // target repository or base directory (default ".")
	DestDir   string // skills parent directory (relative to TargetDir or absolute; default ".agents/skills")
	Force     bool   // overwrite on Install, or create on Update
}

// ResolvePath returns the resolved absolute path of the modguard skill directory.
func ResolvePath(opts Options) (string, error) {
	dest := strings.TrimSpace(opts.DestDir)
	if dest == "" {
		dest = DefaultSkillsDir
	}
	if filepath.IsAbs(dest) {
		return filepath.Join(filepath.Clean(dest), SkillName), nil
	}
	base := strings.TrimSpace(opts.TargetDir)
	if base == "" {
		base = "."
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	return filepath.Join(absBase, filepath.FromSlash(dest), SkillName), nil
}

// Install writes the embedded skill into the target directory.
// It fails if the skill already exists unless opts.Force is true.
func Install(skillFS fs.FS, opts Options) (string, error) {
	target, err := ResolvePath(opts)
	if err != nil {
		return "", err
	}
	exists, err := pathExists(target)
	if err != nil {
		return "", err
	}
	if exists && !opts.Force {
		return "", fmt.Errorf("skill already exists at %s (use \"modguard skill update\" or --force)", target)
	}
	if exists {
		if err := verifyOwnedSkillDir(target); err != nil {
			return "", err
		}
	}
	if err := writeSkillAtomically(skillFS, target); err != nil {
		return "", err
	}
	return target, nil
}

// Update replaces an existing modguard skill installation with the embedded version.
// It fails if the skill is not installed unless opts.Force is true.
func Update(skillFS fs.FS, opts Options) (string, error) {
	target, err := ResolvePath(opts)
	if err != nil {
		return "", err
	}
	exists, err := pathExists(target)
	if err != nil {
		return "", err
	}
	if !exists && !opts.Force {
		return "", fmt.Errorf("skill not installed at %s (use \"modguard skill install\" or --force)", target)
	}
	if exists {
		if err := verifyOwnedSkillDir(target); err != nil {
			return "", err
		}
	}
	if err := writeSkillAtomically(skillFS, target); err != nil {
		return "", err
	}
	return target, nil
}

// Remove deletes an installed modguard skill directory after verifying it is
// a modguard skill directory (never deleting arbitrary unrelated paths).
func Remove(opts Options) (string, error) {
	target, err := ResolvePath(opts)
	if err != nil {
		return "", err
	}
	exists, err := pathExists(target)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", fmt.Errorf("skill not installed at %s", target)
	}
	if err := verifyOwnedSkillDir(target); err != nil {
		return "", err
	}
	if err := os.RemoveAll(target); err != nil {
		return "", err
	}
	return target, nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func verifyOwnedSkillDir(target string) error {
	info, err := os.Lstat(target)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to modify symlinked skill path %s", target)
	}
	if !info.IsDir() {
		return fmt.Errorf("expected directory at %s", target)
	}
	raw, err := os.ReadFile(filepath.Join(target, "SKILL.md"))
	if err != nil {
		return fmt.Errorf("refusing to modify %s: missing SKILL.md (%w)", target, err)
	}
	if !hasModguardFrontmatter(raw) {
		return fmt.Errorf("refusing to modify %s: SKILL.md is not a modguard skill (expected \"name: modguard\")", target)
	}
	return nil
}

func hasModguardFrontmatter(raw []byte) bool {
	text := string(bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n")))
	if !strings.HasPrefix(text, "---\n") {
		return false
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return false
	}
	for _, line := range strings.Split(text[4:4+end], "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "name: modguard" || trimmed == `name: "modguard"` || trimmed == "name: 'modguard'" {
			return true
		}
	}
	return false
}

func writeSkillAtomically(skillFS fs.FS, target string) error {
	sub, err := fs.Sub(skillFS, "skill")
	if err != nil {
		return fmt.Errorf("open embedded skill: %w", err)
	}
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".modguard-skill-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	tree := filepath.Join(staging, SkillName)
	err = fs.WalkDir(sub, ".", func(rel string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		dst := filepath.Join(tree, filepath.FromSlash(rel))
		if d.IsDir() {
			return os.MkdirAll(dst, 0755)
		}
		data, err := fs.ReadFile(sub, rel)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0644)
	})
	if err != nil {
		return err
	}

	backup := filepath.Join(staging, "backup")
	hadExisting := false
	if _, err := os.Lstat(target); err == nil {
		if err := os.Rename(target, backup); err != nil {
			return err
		}
		hadExisting = true
	}
	if err := os.Rename(tree, target); err != nil {
		if hadExisting {
			_ = os.Rename(backup, target)
		}
		return err
	}
	return nil
}
