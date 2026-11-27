package sdk

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
)

// The AST pass covers imports in every source file. The type pass also follows
// inferred values, method sets and private aliases in the selected Go build.
func (c *checker) checkTypes(targets []string) error {
	var patterns []string
	for _, dir := range targets {
		rel, err := filepath.Rel(c.root, dir)
		if err != nil {
			return err
		}
		patterns = append(patterns, "./"+filepath.ToSlash(rel))
	}
	pkgs, err := packages.Load(&packages.Config{Dir: c.root, Mode: packages.LoadSyntax}, patterns...)
	if err != nil {
		return err
	}
	contracts := make(map[string]assemblyContracts)
	for _, pkg := range pkgs {
		sdk, isAssembly := strings.CutSuffix(pkg.PkgPath, "/provider")
		if isAssembly && c.multi[sdk] && !pkg.IllTyped && pkg.Types != nil {
			contracts[sdk] = supplierContracts(pkg, sdk)
		}
	}
	for _, pkg := range pkgs {
		for _, e := range pkg.Errors {
			c.report(e.Pos, 0, "go", e.Msg)
		}
		if pkg.IllTyped || pkg.Types == nil {
			if len(pkg.Errors) == 0 {
				c.report(pkg.PkgPath, 0, "go", "could not type-check package or its dependencies")
			}
			continue
		}
		prefix := c.importPath + "/"
		sdkName, _, _ := strings.Cut(strings.TrimPrefix(pkg.PkgPath, prefix), "/")
		sdk := prefix + sdkName
		if within(pkg.PkgPath, sdk+"/internal") {
			if filepath.ToSlash(filepath.Dir(pkg.PkgPath)) == sdk+"/internal/providers" {
				c.checkSupplier(pkg, contracts[sdk])
			}
			continue
		}
		if pkg.Name == "main" {
			c.report(pkg.PkgPath, 0, "layout", "public SDK must be an importable library")
		}
		c.checkEntrypoint(pkg, sdk, contracts[sdk].public)
		if path := c.missingTypes[pkg.PkgPath]; path != "" {
			c.checkDataTypesFile(pkg.Types.Scope(), path)
		}
		for _, name := range pkg.Types.Scope().Names() {
			obj := pkg.Types.Scope().Lookup(name)
			if !obj.Exported() {
				continue
			}
			leak := internalType(obj.Type(), sdk, make(map[types.Type]bool))
			if leak != "" {
				pos := pkg.Fset.Position(obj.Pos())
				c.report(pos.Filename, pos.Line, "api", name+" exposes internal type "+leak)
			}
		}
		c.checkBindings(pkg, sdk)

	}
	return nil
}

func (c *checker) checkDataTypesFile(scope *types.Scope, path string) {
	errorType := types.Universe.Lookup("error").Type().Underlying().(*types.Interface)
	for _, name := range scope.Names() {
		obj, ok := scope.Lookup(name).(*types.TypeName)
		if !ok || !obj.Exported() || name == "Config" || name == "Client" {
			continue
		}
		t := obj.Type()
		if !types.Implements(t, errorType) && !types.Implements(types.NewPointer(t), errorType) {
			c.report(path, 0, "layout", "required for public data type "+name)
			return
		}
	}
}

// The assembly returns a capability owned by the SDK. A fixed-platform New
// returns the SDK's own client so callers can invoke its operations.
func (c *checker) checkEntrypoint(pkg *packages.Package, sdk string, capabilities []*types.Interface) {
	scope := pkg.Types.Scope()
	if pkg.PkgPath == sdk {
		config, ok := scope.Lookup("Config").(*types.TypeName)
		if !ok {
			c.report(pkg.PkgPath, 0, "layout", "active Config must be a struct")
		} else if _, ok := config.Type().Underlying().(*types.Struct); !ok {
			pos := pkg.Fset.Position(config.Pos())
			c.report(pos.Filename, pos.Line, "layout", "active Config must be a struct")
		}
		if c.multi[sdk] {
			for _, name := range scope.Names() {
				obj := scope.Lookup(name)
				if obj.Exported() && (name == "Client" || name == "New" || returnsProvider(obj, capabilities)) {
					pos := pkg.Fset.Position(obj.Pos())
					c.report(pos.Filename, pos.Line, "layout", "multi-provider root exposes "+name+"; construct clients in provider/")
				}
			}
			return
		}
	}
	single := !c.multi[sdk]
	for _, name := range scope.Names() {
		f, ok := scope.Lookup(name).(*types.Func)
		if !ok || !f.Exported() || (single && name != "New") {
			continue
		}
		if !single {
			if len(factoryCapabilities(f, sdk)) > 0 {
				return
			}
			continue
		}
		for result := range f.Signature().Results().Variables() {
			t := types.Unalias(result.Type())
			if ptr, ok := t.(*types.Pointer); ok {
				t = types.Unalias(ptr.Elem())
			}
			n, ok := t.(*types.Named)
			if !ok || n.Obj().Pkg() == nil || n.Obj().Pkg().Path() != sdk || !n.Obj().Exported() {
				continue
			}
			if n.Obj().Name() != "Client" {
				continue
			}
			for method := range types.NewMethodSet(types.NewPointer(n)).Methods() {
				if method.Obj().Exported() {
					return
				}
			}
		}
	}
	message := "expected a factory returning a public SDK capability interface"
	if single {
		message = "expected New to return a Client with an exported operation"
	}
	if len(pkg.GoFiles) > 0 {
		c.report(filepath.Dir(pkg.GoFiles[0]), 0, "layout", message)
	}
}

// Match callable values as well as functions, including factories that return a concrete implementation.
func returnsProvider(obj types.Object, capabilities []*types.Interface) bool {
	switch obj.(type) {
	case *types.Func, *types.Var:
	default:
		return false
	}
	signature, ok := obj.Type().Underlying().(*types.Signature)
	if !ok {
		return false
	}
	for result := range signature.Results().Variables() {
		for _, capability := range capabilities {
			if types.Implements(result.Type(), capability) {
				return true
			}
		}
	}
	return false
}

// Track direct bindings, including assignments in init and through private variables.
func (c *checker) checkBindings(pkg *packages.Package, sdk string) {
	sources := make(map[types.Object][]ast.Expr)
	var exports []*ast.Ident
	record := func(lhs *ast.Ident, rhs ast.Expr) {
		obj := pkg.TypesInfo.ObjectOf(lhs)
		if obj == nil {
			return
		}
		sources[obj] = append(sources[obj], rhs)
		if obj.Parent() == pkg.Types.Scope() && obj.Exported() {
			exports = append(exports, lhs)
		}
	}
	for _, file := range pkg.Syntax {
		ast.Inspect(file, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.TypeSpec:
				if !v.Assign.IsValid() {
					record(v.Name, v.Type)
				}
			case *ast.ValueSpec:
				if len(v.Values) == len(v.Names) {
					for i, lhs := range v.Names {
						record(lhs, v.Values[i])
					}
				}
			case *ast.AssignStmt:
				if len(v.Lhs) == len(v.Rhs) {
					for i, x := range v.Lhs {
						if lhs := bindingTarget(x); lhs != nil {
							record(lhs, v.Rhs[i])
						}
					}
				}
			}
			return true
		})
	}
	for _, name := range exports {
		root := pkg.TypesInfo.ObjectOf(name)
		seen := map[types.Object]bool{}
		var visit func(types.Object)
		visit = func(obj types.Object) {
			if obj == nil || seen[obj] {
				return
			}
			seen[obj] = true
			if obj.Pkg() != nil && within(obj.Pkg().Path(), sdk+"/internal") {
				pos := pkg.Fset.Position(root.Pos())
				c.report(pos.Filename, pos.Line, "api", root.Name()+" re-exports "+obj.Pkg().Path()+"."+obj.Name())
				return
			}
			for _, expr := range sources[obj] {
				visitBinding(pkg, expr, visit)
			}
		}
		visit(root)
	}
}

// Only exported selectors of a package value can forward a binding to its callers.
func bindingTarget(expr ast.Expr) *ast.Ident {
	switch x := ast.Unparen(expr).(type) {
	case *ast.Ident:
		return x
	case *ast.SelectorExpr:
		if x.Sel.IsExported() {
			return bindingTarget(x.X)
		}
	case *ast.IndexExpr:
		return bindingTarget(x.X)
	}
	return nil
}

func visitBinding(pkg *packages.Package, expr ast.Expr, visit func(types.Object)) {
	switch x := ast.Unparen(expr).(type) {
	case *ast.Ident:
		visit(pkg.TypesInfo.Uses[x])
	case *ast.SelectorExpr:
		visit(pkg.TypesInfo.Uses[x.Sel])
	case *ast.IndexExpr:
		if pkg.TypesInfo.Types[x.Index].IsType() {
			visitBinding(pkg, x.X, visit)
		}
	case *ast.IndexListExpr:
		visitBinding(pkg, x.X, visit)
	case *ast.UnaryExpr:
		if x.Op == token.AND {
			visitBinding(pkg, x.X, visit)
		}
	case *ast.CompositeLit:
		typ := pkg.TypesInfo.TypeOf(x)
		if ptr, ok := typ.Underlying().(*types.Pointer); ok {
			typ = ptr.Elem()
		}
		fields, _ := typ.Underlying().(*types.Struct)
		for i, elem := range x.Elts {
			if kv, ok := elem.(*ast.KeyValueExpr); ok {
				if fields != nil {
					field, ok := kv.Key.(*ast.Ident)
					if !ok || !field.IsExported() {
						continue
					}
				}
				elem = kv.Value
			} else if fields != nil && !fields.Field(i).Exported() {
				continue
			}
			visitBinding(pkg, elem, visit)
		}
	}
}

// Resolve selectors on the outer struct to respect shadowing and ambiguity.
func visibleFields(receiver types.Type, root *types.Struct) []*types.Var {
	var fields []*types.Var
	seen := map[*types.Struct]bool{}
	var visit func(*types.Struct)
	visit = func(s *types.Struct) {
		if seen[s] {
			return
		}
		seen[s] = true
		for f := range s.Fields() {
			if f.Exported() {
				if obj, _, _ := types.LookupFieldOrMethod(receiver, true, nil, f.Name()); obj == f {
					fields = append(fields, f)
				}
			}
			if f.Embedded() {
				t := types.Unalias(f.Type())
				if ptr, ok := t.Underlying().(*types.Pointer); ok {
					t = ptr.Elem()
				}
				if inner, ok := t.Underlying().(*types.Struct); ok {
					visit(inner)
				}
			}
		}
	}
	visit(root)
	return fields
}

func internalType(t types.Type, sdk string, seen map[types.Type]bool) string {
	if t == nil || seen[t] {
		return ""
	}
	seen[t] = true
	var next []types.Type
	var fields *types.Struct
	switch x := t.(type) {
	case *types.Named:
		obj := x.Obj()
		if obj.Pkg() != nil && within(obj.Pkg().Path(), sdk+"/internal") {
			return obj.Pkg().Path() + "." + obj.Name()
		}
		for a := range x.TypeArgs().Types() {
			next = append(next, a)
		}
		if obj.Pkg() != nil && within(obj.Pkg().Path(), sdk) {
			if structure, ok := x.Underlying().(*types.Struct); ok {
				fields = structure
			} else {
				next = append(next, x.Underlying())
			}
			for p := range x.TypeParams().TypeParams() {
				next = append(next, p)
			}
			// Pointer method sets include promoted methods of private embedded fields.
			for sel := range types.NewMethodSet(types.NewPointer(x)).Methods() {
				if sel.Obj().Exported() {
					if p := sel.Obj().Pkg(); p != nil && within(p.Path(), sdk+"/internal") {
						return p.Path() + "." + sel.Obj().Name()
					}
					next = append(next, sel.Type())
				}
			}
		}
	case *types.Alias:
		if obj := x.Obj(); obj.Pkg() != nil && within(obj.Pkg().Path(), sdk+"/internal") {
			return obj.Pkg().Path() + "." + obj.Name()
		}
		for a := range x.TypeArgs().Types() {
			next = append(next, a)
		}
		for p := range x.TypeParams().TypeParams() {
			next = append(next, p)
		}
		next = append(next, x.Rhs())
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
		fields = x
	case *types.Signature:
		for p := range x.TypeParams().TypeParams() {
			next = append(next, p)
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
	case *types.TypeParam:
		next = append(next, x.Constraint())
	case *types.Union:
		for i := 0; i < x.Len(); i++ {
			next = append(next, x.Term(i).Type())
		}
	}
	if fields != nil {
		for _, f := range visibleFields(t, fields) {
			if p := f.Pkg(); p != nil && within(p.Path(), sdk+"/internal") {
				return p.Path() + "." + f.Name()
			}
			next = append(next, f.Type())
		}
	}
	for _, child := range next {
		if leak := internalType(child, sdk, seen); leak != "" {
			return leak
		}
	}
	return ""
}
