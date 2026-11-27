package sdk

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/tools/cover"
	"golang.org/x/tools/go/packages"
)

// Record calls in the isolated consumer itself. Coverage of the SDK alone can
// include methods invoked by package initialization, constructors, or fmt.
func instrumentConsumer(consumer, module, evidence string) error {
	cfg := &packages.Config{
		Dir: consumer, Tests: true, Mode: packages.LoadSyntax,
		Env: append(os.Environ(), "GOWORK=off"),
	}
	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		return err
	}
	testFile := filepath.Join(consumer, "consumer_test.go")
	for _, pkg := range pkgs {
		for _, problem := range pkg.Errors {
			return errors.New(problem.Error())
		}
		for _, source := range pkg.Syntax {
			if pkg.Fset.Position(source.Pos()).Filename != testFile {
				continue
			}
			ast.Inspect(source, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
				if !ok {
					return true
				}
				sel := pkg.TypesInfo.Selections[selector]
				if !isConsumerOperation(sel, module) {
					return true
				}
				name := &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(sel.Obj().Name())}
				call.Fun = &ast.CallExpr{Fun: ast.NewIdent("modguardObserveCall"), Args: []ast.Expr{call.Fun, name}}
				return true
			})
			var out bytes.Buffer
			if err := format.Node(&out, pkg.Fset, source); err != nil {
				return err
			}
			if err := os.WriteFile(testFile, out.Bytes(), 0600); err != nil {
				return err
			}
			// This helper only exists alongside the copy of the external test. A
			// selector in a skipped test or an untaken branch creates no evidence.
			helper := fmt.Sprintf(`package %s
import ("os"; "path/filepath")
func modguardObserveCall[F any](method F, name string) F {
	if err := os.WriteFile(filepath.Join(%q, name), nil, 0600); err != nil { panic(err) }
	return method
}
`, source.Name.Name, evidence)
			return os.WriteFile(filepath.Join(consumer, "modguard_probe_test.go"), []byte(helper), 0600)
		}
	}
	return errors.New("could not type-check external consumer test")
}

func isConsumerOperation(sel *types.Selection, module string) bool {
	if sel == nil || sel.Kind() == types.FieldVal || !sel.Obj().Exported() {
		return false
	}
	recv := types.Unalias(sel.Recv())
	if ptr, ok := recv.(*types.Pointer); ok {
		recv = types.Unalias(ptr.Elem())
	}
	named, ok := recv.(*types.Named)
	if !ok || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != module {
		return false
	}
	// A single-platform Client or a public SDK capability interface is an
	// operation receiver. Return values, config types and errors are not.
	_, iface := named.Underlying().(*types.Interface)
	if named.Obj().Name() != "Client" && !(iface && named.Obj().Exported()) {
		return false
	}
	switch sel.Obj().Name() {
	case "Error", "String", "GoString", "Format", "Unwrap":
		return false
	}
	return true
}

func checkConsumerCoverage(dir, module, profile, evidence string) error {
	calls, err := os.ReadDir(evidence)
	if err != nil {
		return err
	}
	called := make(map[string]bool, len(calls))
	for _, entry := range calls {
		called[entry.Name()] = true
	}
	files, err := cover.ParseProfiles(profile)
	if err != nil {
		return err
	}
	for _, file := range files {
		rel, ok := strings.CutPrefix(file.FileName, module+"/")
		if !ok || !filepath.IsLocal(rel) {
			continue
		}
		set := token.NewFileSet()
		source, err := parser.ParseFile(set, filepath.Join(dir, rel), nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, decl := range source.Decls {
			method, ok := decl.(*ast.FuncDecl)
			if !ok || method.Recv == nil || !called[method.Name.Name] || method.Body == nil {
				continue
			}
			start, end := set.Position(method.Body.Pos()), set.Position(method.Body.End())
			for _, block := range file.Blocks {
				if block.Count > 0 && block.NumStmt > 0 && (block.StartLine > start.Line || block.StartLine == start.Line && block.StartCol >= start.Column) &&
					(block.EndLine < end.Line || block.EndLine == end.Line && block.EndCol <= end.Column) {
					return nil
				}
			}
		}
	}
	return errors.New("external consumer exercised no SDK operation; call a public capability and verify its result")
}
