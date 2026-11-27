// Package modcheck validates standalone Go module boundaries, architectural
// rules, and out-of-repo exportability using composable strictness levels and rules.
package modcheck

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/tools/go/packages"
)

// Level controls which built-in rules modguard check enables.
type Level string

const (
	LevelRelaxed  Level = "relaxed"
	LevelStandard Level = "standard"
	LevelStrict   Level = "strict"
)

// ParseLevel parses a strictness level string. Empty defaults to standard.
func ParseLevel(s string) (Level, error) {
	switch Level(strings.ToLower(strings.TrimSpace(s))) {
	case LevelRelaxed:
		return LevelRelaxed, nil
	case "", LevelStandard:
		return LevelStandard, nil
	case LevelStrict:
		return LevelStrict, nil
	default:
		return "", fmt.Errorf("unknown level %q (use relaxed, standard, or strict)", s)
	}
}

func (l Level) atLeast(other Level) bool {
	rank := map[Level]int{LevelRelaxed: 1, LevelStandard: 2, LevelStrict: 3}
	return rank[l] >= rank[other]
}

// Pair represents a directional rule such as fromDir:toDir or dir:pkgPattern.
type Pair struct {
	Left  string
	Right string
}

// Options configures a module boundary check.
type Options struct {
	Dir             string
	Level           Level
	AllowImports    []string
	ForbidImports   []string
	OnlyImportIn    map[string]string
	ForbidLayers    []Pair
	ForbidCalls     []string
	AllowEnv        []string
	ForbidEnv       bool
	AllowInternalIn []string
	ExcludeDirTests []Pair
	SkipStandalone  bool
	WithTools       bool
	AllTools        bool
	Security        bool
	GCISpec         string
	StaticcheckSpec string
	DeadcodeSpec    string
	GovulncheckSpec string
	GosecSpec       string
}

// Result holds the checked module path and any reported violations.
type Result struct {
	ModulePath string
	Problems   []string
}

var trashPackageNames = map[string]bool{
	"util":    true,
	"utils":   true,
	"common":  true,
	"helper":  true,
	"helpers": true,
	"misc":    true,
	"shared":  true,
	"pkg":     true,
}

var validPkgNameRe = regexp.MustCompile(`^[a-z][a-z0-9]*$`)

var forbiddenInitCalls = map[string]map[string]bool{
	"os":           {"Getenv": true, "LookupEnv": true, "Environ": true},
	"net":          {"Dial": true, "DialTimeout": true, "Listen": true},
	"net/http":     {"Get": true, "Post": true, "Head": true, "ListenAndServe": true},
	"database/sql": {"Open": true},
}

// Check runs all enabled rules for the standalone module at opts.Dir.
func Check(opts Options) (Result, error) {
	if opts.Level == "" {
		opts.Level = LevelStandard
	}
	dir, err := filepath.Abs(opts.Dir)
	if err != nil {
		return Result{}, err
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return Result{}, err
	}
	modFilePath := filepath.Join(dir, "go.mod")
	rawMod, err := os.ReadFile(modFilePath)
	if err != nil {
		return Result{}, fmt.Errorf("read %s: %w", modFilePath, err)
	}
	mf, err := modfile.Parse(modFilePath, rawMod, nil)
	if err != nil {
		return Result{}, fmt.Errorf("parse %s: %w", modFilePath, err)
	}
	if mf.Module == nil || mf.Module.Mod.Path == "" {
		return Result{}, fmt.Errorf("%s: missing module declaration", modFilePath)
	}
	modPath := mf.Module.Mod.Path

	c := &checker{
		dir:      dir,
		modPath:  modPath,
		opts:     opts,
		allowEnv: toSet(opts.AllowEnv),
		forbCall: toSet(opts.ForbidCalls),
	}

	// Discover sibling/host modules in the enclosing repository.
	repo := findRepoRoot(dir)
	c.repoModules, err = discoverSiblingModules(repo, dir, modPath)
	if err != nil {
		return Result{}, err
	}

	// 1. Relaxed: forbid local replace directives in standalone go.mod.
	for _, r := range mf.Replace {
		if r.New.Version == "" {
			c.report("go.mod", 0, "mod/local-replace", fmt.Sprintf("standalone module go.mod must not use local replace %s => %s", r.Old.Path, r.New.Path))
		}
	}

	// 2. Standard+: forbid redundant top-level pkg/ directory (Go official layout).
	if opts.Level.atLeast(LevelStandard) && hasDir(dir, "pkg") {
		c.report("pkg", 0, "layout/no-pkg-dir", "avoid redundant top-level pkg/ directory; place public packages at semantic paths and private code under internal/")
	}

	// 3. Walk all files for symlinks, AST imports, layers, package naming, init() side effects, calls, and env reads.
	fset := token.NewFileSet()
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", ".deploy", "bin":
				return filepath.SkipDir
			}
			// Do not recurse into nested independent Go modules.
			if rel != "." {
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			c.report(rel, 0, "layout/symlink", "symlink cannot be verified as module-owned source")
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			c.report(rel, 0, "syntax", err.Error())
			return nil
		}
		c.checkFile(fset, rel, f)
		return nil
	})
	if err != nil {
		return Result{}, err
	}

	// 4. Standard+: public API internal-type leak check via go/types.
	if opts.Level.atLeast(LevelStandard) {
		if err := c.checkPublicAPILeaks(); err != nil {
			return Result{}, err
		}
	}

	// 5. Optional external tool checks (--with-tools or explicit tool flags).
	c.runExternalTools()

	// 6. Strict (or explicit --exclude-dir-test), unless --skip-standalone: out-of-repo standalone export verification.
	runStandalone := (opts.Level.atLeast(LevelStrict) || len(opts.ExcludeDirTests) > 0) && !opts.SkipStandalone
	if runStandalone && len(c.problems) == 0 {
		if err := checkStandaloneModule(dir, repo, modPath, opts.ExcludeDirTests); err != nil {
			c.report(".", 0, "standalone/export", err.Error())
		}
	}

	slices.Sort(c.problems)
	return Result{
		ModulePath: modPath,
		Problems:   slices.Compact(c.problems),
	}, nil
}

type checker struct {
	dir         string
	modPath     string
	repoModules []string
	opts        Options
	allowEnv    map[string]bool
	forbCall    map[string]bool
	problems    []string
}

func (c *checker) report(rel string, line int, rule, msg string) {
	loc := filepath.ToSlash(rel)
	if line > 0 {
		loc = fmt.Sprintf("%s:%d", loc, line)
	}
	c.problems = append(c.problems, fmt.Sprintf("%s [%s] %s", loc, rule, msg))
}

func (c *checker) checkFile(fset *token.FileSet, rel string, f *ast.File) {
	pkgDir := filepath.ToSlash(filepath.Dir(rel))
	isTest := strings.HasSuffix(rel, "_test.go")
	pkgName := f.Name.Name

	// Standard+: forbid trash package names, non-idiomatic package names, and dir/package mismatch.
	if c.opts.Level.atLeast(LevelStandard) && !isTest && pkgName != "main" {
		pkgLine := fset.Position(f.Name.Pos()).Line
		if trashPackageNames[pkgName] {
			c.report(rel, pkgLine, "layout/no-trash-pkg",
				fmt.Sprintf("avoid generic package name %q; name packages by responsibility", pkgName))
		} else if !validPkgNameRe.MatchString(pkgName) {
			c.report(rel, pkgLine, "layout/pkg-naming",
				fmt.Sprintf("package name %q must be lowercase ASCII letters/digits without underscores", pkgName))
		}
		if pkgDir != "." && !within(pkgDir, "testdata") {
			dirBase := filepath.Base(pkgDir)
			if pkgName != dirBase {
				c.report(rel, pkgLine, "layout/pkg-dir-match",
					fmt.Sprintf("package name %q does not match directory name %q", pkgName, dirBase))
			}
		}
	}

	// Map import aliases -> import path for init/env checks.
	importAlias := map[string]string{}
	for _, imp := range f.Imports {
		dep, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			c.report(rel, fset.Position(imp.Pos()).Line, "import/syntax", err.Error())
			continue
		}
		line := fset.Position(imp.Pos()).Line
		localName := filepath.Base(dep)
		if imp.Name != nil {
			localName = imp.Name.Name
		}
		importAlias[localName] = dep

		// Relaxed: no relative imports.
		if dep == "." || dep == ".." || strings.HasPrefix(dep, "./") || strings.HasPrefix(dep, "../") {
			c.report(rel, line, "import/relative", "relative package import is forbidden: "+dep)
			continue
		}

		// Relaxed: no importing sibling/host repo modules unless explicitly allowed.
		if !isTest && !within(dep, c.modPath) && !matchesAny(dep, c.opts.AllowImports) {
			for _, sibling := range c.repoModules {
				if within(dep, sibling) {
					c.report(rel, line, "import/cross-module", "module imports sibling/host package: "+dep)
					break
				}
			}
		}

		// Explicit forbidden imports (--forbid-import).
		if !isTest && matchesAny(dep, c.opts.ForbidImports) {
			c.report(rel, line, "import/forbidden", "forbidden import: "+dep)
		}

		// Explicit layer boundaries (--forbid-layer).
		for _, layer := range c.opts.ForbidLayers {
			if within(pkgDir, layer.Left) && within(dep, c.modPath+"/"+layer.Right) {
				c.report(rel, line, "layer/isolation",
					fmt.Sprintf("package in %s/ must not import %s/ (%s)", layer.Left, layer.Right, dep))
			}
		}

		// Explicit restricted external imports (--only-import-in).
		if !isTest {
			for pkgPrefix, allowedDir := range c.opts.OnlyImportIn {
				if matchesPrefix(dep, pkgPrefix) && !within(pkgDir, allowedDir) {
					c.report(rel, line, "import/restricted",
						fmt.Sprintf("import %s is only allowed under %s/", dep, allowedDir))
				}
			}
		}
	}

	if isTest {
		return
	}

	checkInit := c.opts.Level.atLeast(LevelStandard) && pkgName != "main"
	checkCalls := len(c.forbCall) > 0
	checkEnv := c.opts.ForbidEnv || len(c.allowEnv) > 0
	if !checkInit && !checkCalls && !checkEnv {
		return
	}

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Body == nil {
				continue
			}
			if checkInit && d.Recv == nil && d.Name.Name == "init" {
				c.checkInitNode(fset, rel, d.Body, importAlias)
			}
			if checkCalls || checkEnv {
				c.checkCallsAndEnv(fset, rel, d.Body, importAlias, checkCalls, checkEnv)
			}
		case *ast.GenDecl:
			if d.Tok != token.VAR {
				continue
			}
			for _, spec := range d.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, val := range vs.Values {
					if checkInit {
						c.checkInitNode(fset, rel, val, importAlias)
					}
					if checkCalls || checkEnv {
						c.checkCallsAndEnv(fset, rel, val, importAlias, checkCalls, checkEnv)
					}
				}
			}
		}
	}
}

func (c *checker) checkCallsAndEnv(fset *token.FileSet, rel string, root ast.Node, aliases map[string]string, checkCalls, checkEnv bool) {
	ast.Inspect(root, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if checkCalls && c.forbCall[sel.Sel.Name] {
			c.report(rel, fset.Position(sel.Sel.Pos()).Line, "call/forbidden",
				fmt.Sprintf("forbidden direct call %s", sel.Sel.Name))
		}
		if checkEnv {
			c.checkEnvCall(fset, rel, call, sel, aliases)
		}
		return true
	})
}

func (c *checker) checkInitNode(fset *token.FileSet, rel string, root ast.Node, aliases map[string]string) {
	ast.Inspect(root, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		impPath := aliases[ident.Name]
		if funcs, ok := forbiddenInitCalls[impPath]; ok && funcs[sel.Sel.Name] {
			c.report(rel, fset.Position(call.Pos()).Line, "init/no-side-effects",
				fmt.Sprintf("package initialization in non-main package must not call %s.%s", filepath.Base(impPath), sel.Sel.Name))
		}
		return true
	})
}

func (c *checker) checkEnvCall(fset *token.FileSet, rel string, call *ast.CallExpr, sel *ast.SelectorExpr, aliases map[string]string) {
	ident, ok := sel.X.(*ast.Ident)
	if !ok || aliases[ident.Name] != "os" {
		return
	}
	line := fset.Position(call.Pos()).Line
	switch sel.Sel.Name {
	case "Environ":
		if c.opts.ForbidEnv {
			c.report(rel, line, "env/forbidden", "os.Environ() is forbidden by --forbid-env")
		} else {
			c.report(rel, line, "env/allowlist", "os.Environ() is forbidden when --allow-env is enforced")
		}
	case "Getenv", "LookupEnv":
		if c.opts.ForbidEnv {
			c.report(rel, line, "env/forbidden", "os."+sel.Sel.Name+" is forbidden by --forbid-env")
			return
		}
		if len(call.Args) != 1 {
			return
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			c.report(rel, line, "env/allowlist", "os."+sel.Sel.Name+" must use a string literal key in --allow-env")
			return
		}
		key, err := strconv.Unquote(lit.Value)
		if err != nil {
			return
		}
		if !c.allowEnv[key] {
			c.report(rel, line, "env/allowlist", fmt.Sprintf("environment variable %q is not in --allow-env", key))
		}
	}
}

func (c *checker) checkPublicAPILeaks() error {
	cfg := &packages.Config{
		Dir:  c.dir,
		Mode: packages.LoadSyntax,
		Env:  append(os.Environ(), "GOWORK=off"),
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return err
	}
	for _, pkg := range pkgs {
		if pkg.IllTyped || pkg.Types == nil || pkg.Name == "main" {
			continue
		}
		if within(pkg.PkgPath, c.modPath+"/internal") {
			continue
		}
		relPkg := "."
		if pkg.PkgPath != c.modPath {
			relPkg = strings.TrimPrefix(pkg.PkgPath, c.modPath+"/")
		}
		if matchesAny(relPkg, c.opts.AllowInternalIn) {
			continue
		}
		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			if !obj.Exported() {
				continue
			}
			if leak := findInternalType(obj.Type(), c.modPath, c.modPath+"/internal", make(map[types.Type]bool)); leak != "" {
				pos := pkg.Fset.Position(obj.Pos())
				rel, err := filepath.Rel(c.dir, pos.Filename)
				if err != nil {
					rel = pos.Filename
				}
				c.report(rel, pos.Line, "api/no-internal-leak", fmt.Sprintf("exported %s exposes internal type %s", name, leak))
			}
		}
	}
	return nil
}

func findInternalType(t types.Type, modPath, internalPrefix string, seen map[types.Type]bool) string {
	if t == nil || seen[t] {
		return ""
	}
	seen[t] = true
	var next []types.Type
	switch x := t.(type) {
	case *types.Named:
		if obj := x.Obj(); obj.Pkg() != nil {
			pkgPath := obj.Pkg().Path()
			if within(pkgPath, internalPrefix) {
				return pkgPath + "." + obj.Name()
			}
			if within(pkgPath, modPath) {
				next = append(next, x.Underlying())
				mset := types.NewMethodSet(types.NewPointer(x))
				for i := 0; i < mset.Len(); i++ {
					m := mset.At(i).Obj()
					if m.Exported() {
						next = append(next, m.Type())
					}
				}
			}
		}
		for a := range x.TypeArgs().Types() {
			next = append(next, a)
		}
		for tp := range x.TypeParams().TypeParams() {
			next = append(next, tp.Constraint())
		}
	case *types.Alias:
		if obj := x.Obj(); obj.Pkg() != nil && within(obj.Pkg().Path(), internalPrefix) {
			return obj.Pkg().Path() + "." + obj.Name()
		}
		next = append(next, x.Rhs())
	case *types.TypeParam:
		next = append(next, x.Constraint())
	case *types.Union:
		for term := range x.Terms() {
			next = append(next, term.Type())
		}
	case *types.Pointer:
		next = append(next, x.Elem())
	case *types.Array:
		next = append(next, x.Elem())
	case *types.Slice:
		next = append(next, x.Elem())
	case *types.Chan:
		next = append(next, x.Elem())
	case *types.Map:
		next = append(next, x.Key(), x.Elem())
	case *types.Struct:
		for f := range x.Fields() {
			if f.Exported() {
				next = append(next, f.Type())
			}
		}
	case *types.Signature:
		for tp := range x.TypeParams().TypeParams() {
			next = append(next, tp.Constraint())
		}
		for v := range x.Params().Variables() {
			next = append(next, v.Type())
		}
		for v := range x.Results().Variables() {
			next = append(next, v.Type())
		}
	case *types.Interface:
		for i := 0; i < x.NumEmbeddeds(); i++ {
			next = append(next, x.EmbeddedType(i))
		}
		for m := range x.Methods() {
			if m.Exported() {
				next = append(next, m.Type())
			}
		}
	}
	for _, child := range next {
		if leak := findInternalType(child, modPath, internalPrefix, seen); leak != "" {
			return leak
		}
	}
	return ""
}

const (
	defaultGCISpec         = "github.com/daixiang0/gci@v0.13.7"
	defaultStaticcheckSpec = "honnef.co/go/tools/cmd/staticcheck@v0.8.2"
	defaultDeadcodeSpec    = "golang.org/x/tools/cmd/deadcode@v0.52.0"
	defaultGovulncheckSpec = "golang.org/x/vuln/cmd/govulncheck@v1.10.0"
	defaultGosecSpec       = "github.com/securego/gosec/v2/cmd/gosec@v2.30.0"
)

func (c *checker) runExternalTools() {
	if c.opts.WithTools || c.opts.AllTools {
		if out, err := runCmd(c.dir, "gofmt", "-l", "."); err != nil {
			c.report(".", 0, "tools/gofmt", err.Error())
		} else if trimmed := strings.TrimSpace(out); trimmed != "" {
			c.report(".", 0, "tools/gofmt", "unformatted Go files: "+strings.ReplaceAll(trimmed, "\n", ", "))
		}
		if _, err := runCmd(c.dir, "go", "vet", "./..."); err != nil {
			c.report(".", 0, "tools/vet", err.Error())
		}
	}
	if spec := resolveToolSpec(c.opts.GCISpec, c.opts.AllTools, defaultGCISpec); spec != "" {
		if out, err := runToolSpec(c.dir, spec,
			"list", "--skip-generated", "--skip-vendor",
			"-s", "standard", "-s", "default", "-s", "prefix("+c.modPath+")", ".",
		); err != nil {
			c.report(".", 0, "tools/gci", err.Error())
		} else if trimmed := strings.TrimSpace(out); trimmed != "" {
			c.report(".", 0, "tools/gci", "ungrouped Go imports: "+strings.ReplaceAll(trimmed, "\n", ", "))
		}
	}
	if spec := resolveToolSpec(c.opts.StaticcheckSpec, c.opts.AllTools, defaultStaticcheckSpec); spec != "" {
		if _, err := runToolSpec(c.dir, spec, "-checks=all", "./..."); err != nil {
			c.report(".", 0, "tools/staticcheck", err.Error())
		}
	}
	if spec := resolveToolSpec(c.opts.DeadcodeSpec, c.opts.AllTools, defaultDeadcodeSpec); spec != "" {
		if out, err := runToolSpec(c.dir, spec, "./..."); err != nil {
			c.report(".", 0, "tools/deadcode", err.Error())
		} else if trimmed := strings.TrimSpace(out); trimmed != "" {
			c.report(".", 0, "tools/deadcode", "unreachable functions:\n"+trimmed)
		}
	}
	if spec := resolveToolSpec(c.opts.GovulncheckSpec, c.opts.Security, defaultGovulncheckSpec); spec != "" {
		if _, err := runToolSpec(c.dir, spec, "./..."); err != nil {
			c.report(".", 0, "security/govulncheck", err.Error())
		}
	}
	if spec := resolveToolSpec(c.opts.GosecSpec, c.opts.Security, defaultGosecSpec); spec != "" {
		if _, err := runToolSpec(c.dir, spec, "-quiet", "-exclude=G104", "./..."); err != nil {
			c.report(".", 0, "security/gosec", err.Error())
		}
	}
}

// resolveToolSpec resolves an explicit tool spec or auto-enables it when enabled
// is true. "auto"/"default" uses the verified pinned go run spec so it always
// builds with the active Go toolchain (avoiding stale binaries on PATH).
func resolveToolSpec(spec string, enabled bool, fallbackSpec string) string {
	s := strings.TrimSpace(spec)
	if s == "" {
		if !enabled {
			return ""
		}
		return fallbackSpec
	}
	if strings.EqualFold(s, "auto") || strings.EqualFold(s, "default") {
		return fallbackSpec
	}
	return s
}

// runToolSpec runs either `go run <spec> <args...>` when spec contains '@'
// (versioned Go package spec), or executes spec directly as a binary path/name.
func runToolSpec(dir, spec string, args ...string) (string, error) {
	if strings.Contains(spec, "@") {
		goArgs := append([]string{"run", spec}, args...)
		return runCmd(dir, "go", goArgs...)
	}
	return runCmd(dir, spec, args...)
}

func hasDir(root, name string) bool {
	info, err := os.Stat(filepath.Join(root, name))
	return err == nil && info.IsDir()
}

func checkStandaloneModule(dir, repo, modPath string, excludeTests []Pair) error {
	tempRoot, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return err
	}
	if rel, err := filepath.Rel(repo, tempRoot); err == nil && (rel == "." || filepath.IsLocal(rel)) {
		return errors.New("standalone export checks require a temp directory outside the repository")
	}
	tmp, err := os.MkdirTemp("", "modguard-standalone-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	dest := filepath.Join(tmp, "mod")
	if err := copyModuleTree(dir, dest); err != nil {
		return err
	}
	if _, err := runCmd(dest, "go", "test", "-mod=readonly", "-count=1", "./..."); err != nil {
		return fmt.Errorf("standalone export of %s failed: %w", modPath, err)
	}
	for _, pair := range excludeTests {
		relDir := filepath.Clean(filepath.FromSlash(pair.Left))
		if relDir == "." || !filepath.IsLocal(relDir) {
			return fmt.Errorf("invalid --exclude-dir-test directory %q: must be a relative path inside module", pair.Left)
		}
		subTmp, err := os.MkdirTemp("", "modguard-exclude-")
		if err != nil {
			return err
		}
		subDest := filepath.Join(subTmp, "mod")
		err = copyModuleTree(dir, subDest)
		if err == nil {
			err = os.RemoveAll(filepath.Join(subDest, relDir))
		}
		if err == nil {
			_, err = runCmd(subDest, "go", "test", "-mod=readonly", "-count=1", pair.Right)
		}
		os.RemoveAll(subTmp)
		if err != nil {
			return fmt.Errorf("standalone export without %s (%s) failed: %w", pair.Left, pair.Right, err)
		}
	}
	return nil
}

func copyModuleTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", ".deploy", "bin":
				return filepath.SkipDir
			}
			if rel != "." {
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return filepath.SkipDir
				}
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0755)
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, info.Mode().Perm())
	})
}

func discoverSiblingModules(repo, selfDir, selfMod string) ([]string, error) {
	var mods []string
	err := filepath.WalkDir(repo, func(path string, d fs.DirEntry, err error) error {
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
		if d.Name() != "go.mod" {
			return nil
		}
		modDir := filepath.Dir(path)
		if modDir == selfDir {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		name := modfile.ModulePath(data)
		if name != "" && name != selfMod {
			mods = append(mods, name)
		}
		return nil
	})
	return mods, err
}

func findRepoRoot(dir string) string {
	for p := dir; ; p = filepath.Dir(p) {
		if _, err := os.Stat(filepath.Join(p, ".git")); err == nil {
			return p
		}
		if filepath.Dir(p) == p {
			return dir
		}
	}
}

func runCmd(dir, bin string, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "GOWORK=off")
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("%s %s: %w\n%s%s", bin, strings.Join(args, " "), err, out, exit.Stderr)
		}
		return "", err
	}
	return string(out), nil
}

func within(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+"/")
}

func matchesPrefix(dep, prefix string) bool {
	if strings.HasSuffix(prefix, "/") {
		return strings.HasPrefix(dep, prefix)
	}
	return within(dep, prefix)
}

func matchesAny(dep string, prefixes []string) bool {
	for _, p := range prefixes {
		if matchesPrefix(dep, p) {
			return true
		}
	}
	return false
}

func toSet(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[it] = true
	}
	return m
}
