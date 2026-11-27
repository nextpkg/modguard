// Package sdk generates Go SDKs and validates their layout, API boundaries, and standalone exports.
package sdk

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/mod/modfile"
)

type checker struct {
	root         string
	importPath   string
	repoModules  []string
	multi        map[string]bool
	missingTypes map[string]string
	problems     []string
}

// Check validates all SDK packages under dir and optionally runs standalone
// out-of-repo unit and consumer coverage tests.
func Check(dir string, runStandalone bool, stdout, stderr io.Writer) error {
	if stdout == nil {
		stdout = os.Stdout
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	count, problems, err := check(dir)
	if err != nil {
		return err
	}
	for _, problem := range problems {
		fmt.Fprintln(stderr, problem)
	}
	if len(problems) > 0 {
		return fmt.Errorf("modguard sdk check: %d violation(s)", len(problems))
	}
	if runStandalone {
		if err := checkStandaloneSDKs(dir, stdout); err != nil {
			return err
		}
	}
	fmt.Fprintf(stdout, "modguard sdk check: %d SDK(s) passed\n", count)
	return nil
}

func check(dir string) (int, []string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return 0, nil, err
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return 0, nil, err
	}
	host, err := loadSDKModule(dir)
	if err != nil {
		return 0, nil, err
	}
	c := checker{root: host.root, importPath: host.importPath, multi: make(map[string]bool), missingTypes: make(map[string]string)}
	repo := repositoryRoot(c.root)
	var versionReplacements []*modfile.Replace
	err = filepath.WalkDir(repo, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		switch d.Name() {
		case "go.mod", "go.work":
		default:
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var replacements []*modfile.Replace
		if d.Name() == "go.work" {
			work, err := modfile.ParseWork(path, data, nil)
			if err != nil {
				return err
			}
			replacements = work.Replace
		} else {
			name := modfile.ModulePath(data)
			if name == "" {
				return fmt.Errorf("%s: missing module declaration", path)
			}
			c.repoModules = append(c.repoModules, name)
			// A module whose path mirrors its repository directory also identifies
			// the canonical repository prefix (the host module may have a short name).
			rel, err := filepath.Rel(repo, filepath.Dir(path))
			if err != nil {
				return err
			}
			if prefix, ok := strings.CutSuffix(name, "/"+filepath.ToSlash(rel)); ok && strings.Count(prefix, "/") >= 2 {
				c.repoModules = append(c.repoModules, prefix)
			}
			if filepath.Dir(path) == c.root {
				replacements = host.manifest.Replace
			}
		}
		for _, r := range replacements {
			if r.New.Version != "" {
				versionReplacements = append(versionReplacements, r)
				continue
			}
			dest := r.New.Path
			if !filepath.IsAbs(dest) {
				dest = filepath.Join(filepath.Dir(path), dest)
			}
			if real, err := filepath.EvalSymlinks(dest); err == nil {
				dest = real
			}
			if within(filepath.ToSlash(filepath.Clean(dest)), filepath.ToSlash(repo)) {
				c.repoModules = append(c.repoModules, r.Old.Path)
			}
		}
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	for _, r := range versionReplacements {
		for _, mod := range c.repoModules {
			if within(r.New.Path, mod) {
				c.repoModules = append(c.repoModules, r.Old.Path)
				break
			}
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, nil, err
	}
	var targets []string
	count := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if !entry.IsDir() {
			if entry.Type()&fs.ModeSymlink != 0 || filepath.Ext(path) == ".go" {
				c.report(path, 0, "layout", "expected an SDK directory")
			}
			continue
		}
		if entry.Name() == "cmd" {
			c.report(path, 0, "layout", "SDK collection directory must not contain cmd/; place CLI tooling in a separate module")
			continue
		}
		count++
		parts, err := c.scan(path)
		if err != nil {
			return 0, nil, err
		}
		targets = append(targets, parts...)
	}
	if len(targets) > 0 {
		if err := c.checkTypes(targets); err != nil {
			return 0, nil, err
		}
	}
	slices.Sort(c.problems)
	return count, slices.Compact(c.problems), nil
}

func repositoryRoot(dir string) string {
	for p := dir; ; p = filepath.Dir(p) {
		if _, err := os.Stat(filepath.Join(p, ".git")); err == nil {
			return p
		}
		if filepath.Dir(p) == p {
			return dir
		}
	}
}

func (c *checker) report(path string, line int, rule, message string) {
	if filepath.IsAbs(path) {
		if relative, err := filepath.Rel(c.root, path); err == nil {
			path = relative
		}
	}
	if line > 0 {
		path = fmt.Sprintf("%s:%d", path, line)
	}
	c.problems = append(c.problems, fmt.Sprintf("%s [%s] %s", filepath.ToSlash(path), rule, message))
}

func within(path, root string) bool { return path == root || strings.HasPrefix(path, root+"/") }
