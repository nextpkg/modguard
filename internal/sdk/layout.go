package sdk

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
)

// scan checks source imports regardless of build tags, including test files.
func (c *checker) scan(dir string) ([]string, error) {
	files := map[string]*ast.File{}
	dirs := map[string]bool{}
	packages := map[string]bool{}
	sdkName := filepath.Base(dir)
	if !validPackage(sdkName) {
		c.report(dir, 0, "layout", "invalid SDK package name "+strconv.Quote(sdkName))
	}
	importRoot := c.importPath + "/" + sdkName
	packageNames := map[string]string{".": sdkName}
	// WalkDir visits each directory before its files. Only directories owned
	// by the layout acquire a required package clause.
	var suppliers string
	for _, rule := range layouts["multi"] {
		switch rule.Role {
		case "factory":
			factory := rule.directory()
			packageNames[factory] = filepath.Base(factory)
		case "supplier":
			suppliers = rule.directory()
		}
	}
	fset := token.NewFileSet()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.Type()&fs.ModeSymlink != 0 {
			c.report(path, 0, "layout", "symlink cannot be verified as SDK-owned source")
			return nil
		}
		if d.IsDir() {
			dirs[rel] = true
			if filepath.ToSlash(filepath.Dir(rel)) == suppliers {
				name := d.Name()
				if !validPackage(name) {
					c.report(path, 0, "layout", "invalid supplier package name "+strconv.Quote(name))
				}
				packageNames[rel] = name
			}
			return nil
		}
		if d.Name() == "go.mod" {
			c.report(path, 0, "layout", "SDK templates share the parent Go module")
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		pkgDir := filepath.ToSlash(filepath.Dir(rel))
		if !within(rel, "testdata") {
			packages[pkgDir] = true
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			c.report(path, 0, "syntax", err.Error())
			return nil
		}
		if want, owned := packageNames[pkgDir]; owned && f.Name.Name != want {
			isTest := strings.HasSuffix(rel, "_test.go")
			if !isTest || f.Name.Name != want+"_test" {
				if isTest {
					want += " or " + want + "_test"
				}
				c.report(path, fset.Position(f.Name.Pos()).Line, "layout", "expected package "+want+", got "+f.Name.Name)
			}
		}
		for _, imp := range f.Imports {
			name, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				c.report(path, fset.Position(imp.Pos()).Line, "import", err.Error())
				continue
			}
			if name == ".." || name == "." || strings.HasPrefix(name, "../") || strings.HasPrefix(name, "./") {
				c.report(path, fset.Position(imp.Pos()).Line, "import", "relative package import: "+name)
				continue
			}
			if within(name, importRoot) {
				continue
			}
			for _, mod := range c.repoModules {
				if within(name, mod) {
					c.report(path, fset.Position(imp.Pos()).Line, "import", "SDK imports another repository package: "+name)
					break
				}
			}
		}
		if !strings.HasSuffix(rel, "_test.go") {
			files[rel] = f
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	kind := "single"
	for _, rule := range layouts["multi"] {
		if rule.directory() == "." && files[rule.Path] != nil || rule.directory() != "." && dirs[rule.directory()] {
			kind = "multi"
			break
		}
	}
	multi := kind == "multi"
	c.multi[importRoot] = multi
	publicDirs := map[string]bool{".": true}
	required := func(name string) *ast.File {
		f := files[name]
		if f == nil {
			c.report(filepath.Join(dir, name), 0, "layout", "required Go file is missing or invalid")
		}
		return f
	}
	requireDecl := func(name, what string, match func(ast.Decl) bool) {
		if f := required(name); f != nil && !hasDeclaration(f, match) {
			c.report(filepath.Join(dir, name), 1, "layout", "expected "+what)
		}
	}
	for _, rule := range layoutFor(kind) {
		switch rule.Role {
		case "types":
			if !multi && files[rule.Path] == nil {
				// The active public API determines whether this single-platform SDK needs data types.
				c.missingTypes[importRoot] = filepath.Join(dir, rule.Path)
			} else {
				required(rule.Path)
			}
		case "config":
			requireDecl(rule.Path, "type Config struct", func(d ast.Decl) bool {
				return hasType(d, "Config", func(t ast.Expr) bool { _, ok := t.(*ast.StructType); return ok })
			})
		case "interface":
			requireDecl(rule.Path, "an exported capability interface", func(d ast.Decl) bool {
				return hasType(d, "", func(t ast.Expr) bool { _, ok := t.(*ast.InterfaceType); return ok })
			})
		case "factory":
			pkg := rule.directory()
			publicDirs[pkg] = true
			if !anyFile(files, pkg, publicFunction) {
				c.report(filepath.Join(dir, pkg), 0, "layout", "expected a provider assembly directory with an exported function")
			}
		case "supplier":
			parent := rule.directory()
			count := 0
			for name := range dirs {
				if filepath.ToSlash(filepath.Dir(name)) != parent {
					continue
				}
				count++
				required(rule.file(filepath.Base(name)))
				if !anyFile(files, name, operation) {
					c.report(filepath.Join(dir, name), 0, "layout", "expected a supplier operation with an implementation")
				}
			}
			if count == 0 {
				c.report(filepath.Join(dir, parent), 0, "layout", "expected at least one supplier directory")
			}
		case "client":
			requireDecl(rule.Path, "type Client", func(d ast.Decl) bool { return hasType(d, "Client", nil) })
			requireDecl(rule.Path, "constructor New", func(d ast.Decl) bool {
				fn, ok := d.(*ast.FuncDecl)
				return ok && fn.Recv == nil && fn.Name.Name == "New" && fn.Body != nil
			})
		default:
			required(rule.Path)
		}
	}
	for name := range packages {
		if !publicDirs[name] && !within(name, "internal") {
			c.report(filepath.Join(dir, name), 0, "layout", "unexpected public package (private implementations belong under internal/)")
		}
	}
	// Go's recursive pattern also loads buildable, unwired supplier packages.
	targets := []string{filepath.Join(dir, "...")}
	for pkg := range publicDirs {
		if packages[pkg] {
			targets = append(targets, filepath.Join(dir, pkg))
		}
	}
	return targets, nil
}

func hasDeclaration(f *ast.File, match func(ast.Decl) bool) bool {
	for _, d := range f.Decls {
		if match(d) {
			return true
		}
	}
	return false
}

func hasType(d ast.Decl, name string, match func(ast.Expr) bool) bool {
	g, ok := d.(*ast.GenDecl)
	if !ok || g.Tok != token.TYPE {
		return false
	}
	for _, s := range g.Specs {
		t := s.(*ast.TypeSpec)
		if t.Name.IsExported() && (name == "" || t.Name.Name == name) && (match == nil || match(t.Type)) {
			return true
		}
	}
	return false
}

func anyFile(files map[string]*ast.File, dir string, match func(ast.Decl) bool) bool {
	for file, f := range files {
		if filepath.ToSlash(filepath.Dir(file)) == dir && hasDeclaration(f, match) {
			return true
		}
	}
	return false
}

func publicFunction(d ast.Decl) bool {
	fn, ok := d.(*ast.FuncDecl)
	return ok && fn.Recv == nil && fn.Name.IsExported() && fn.Body != nil
}

func operation(d ast.Decl) bool {
	fn, ok := d.(*ast.FuncDecl)
	return ok && fn.Recv != nil && fn.Name.IsExported() && fn.Body != nil
}
