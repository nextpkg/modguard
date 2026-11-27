package sdk

import (
	"go/types"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// Each alternative represents a public capability or the complete set of private
// supplier interfaces used by one implementation of that capability.
type contractSet []types.Type

type assemblyContracts struct {
	public       []*types.Interface
	alternatives []contractSet
	constructed  map[string][]types.Type
}

// A facade may embed smaller SDK capabilities that callers can also construct separately.
func (a *assemblyContracts) addPublicCapability(t types.Type, sdk string) {
	n, ok := types.Unalias(t).(*types.Named)
	if !ok || n.Obj().Pkg() == nil || n.Obj().Pkg().Path() != sdk || !n.Obj().Exported() {
		return
	}
	iface, ok := n.Underlying().(*types.Interface)
	if !ok || iface.NumMethods() == 0 || slices.Contains(a.public, iface) {
		return
	}
	a.public = append(a.public, iface)
	for i := 0; i < iface.NumEmbeddeds(); i++ {
		a.addPublicCapability(iface.EmbeddedType(i), sdk)
	}
}

func (c *checker) checkSupplier(pkg *packages.Package, assembly assemblyContracts) {
	candidates := assembly.constructed[pkg.PkgPath]
	scope := pkg.Types.Scope()
	if len(candidates) == 0 {
		// An unwired supplier has no call site yet. Use the entry point supplied by the template.
		if factory, ok := scope.Lookup("New").(*types.Func); ok {
			for r := range factory.Signature().Results().Variables() {
				candidates = append(candidates, r.Type())
			}
		} else if client, ok := scope.Lookup("Client").(*types.TypeName); ok {
			candidates = append(candidates, types.NewPointer(client.Type()))
		} else {
			// Suppliers without a conventional factory can expose a directly constructible value.
			for _, name := range scope.Names() {
				if obj, ok := scope.Lookup(name).(*types.TypeName); ok && obj.Exported() {
					if _, iface := obj.Type().Underlying().(*types.Interface); !iface {
						candidates = append(candidates, types.NewPointer(obj.Type()))
					}
				}
			}
		}
	}
	alternatives := assembly.alternatives
	for _, t := range candidates {
		for _, contracts := range alternatives {
			if implementsAll(t, contracts) {
				return
			}
		}
	}
	if len(alternatives) > 0 {
		var names []string
		for _, group := range alternatives {
			var all []string
			for _, t := range group {
				all = append(all, t.String())
			}
			slices.Sort(all)
			names = append(names, strings.Join(all, " + "))
		}
		slices.Sort(names)
		c.report(pkg.PkgPath, 0, "layout", "supplier does not implement an assembly contract: "+strings.Join(slices.Compact(names), " or "))
	}
}

func implementsAll(t types.Type, group contractSet) bool {
	for _, contract := range group {
		if !types.Implements(t, contract.Underlying().(*types.Interface)) {
			return false
		}
	}
	return len(group) > 0
}

func supplierContracts(assembly *packages.Package, sdk string) assemblyContracts {
	prog, pkgs := ssautil.Packages([]*packages.Package{assembly}, 0)
	pkgs[0].Build()
	result := assemblyContracts{constructed: make(map[string][]types.Type)}
	for _, name := range assembly.Types.Scope().Names() {
		f, ok := assembly.Types.Scope().Lookup(name).(*types.Func)
		if !ok || !f.Exported() {
			continue
		}
		for _, capability := range factoryCapabilities(f, sdk) {
			result.alternatives = append(result.alternatives, contractSet{capability})
			iface := capability.Underlying().(*types.Interface)
			result.addPublicCapability(capability, sdk)
			receivers := map[types.Type]bool{}
			vendorInterfaces := map[types.Type]bool{}
			record := func(ins ssa.Instruction, fromFactory bool) {
				if call, ok := ins.(*ssa.Call); ok {
					callee := call.Common().StaticCallee()
					if callee != nil && callee.Pkg != nil && within(callee.Pkg.Pkg.Path(), sdk+"/internal/providers") {
						path := callee.Pkg.Pkg.Path()
						for r := range callee.Signature.Results().Variables() {
							t := types.Unalias(r.Type())
							if types.Identical(t, types.Universe.Lookup("error").Type()) {
								continue
							}
							result.constructed[path] = append(result.constructed[path], t)
							if _, ok := t.Underlying().(*types.Interface); ok {
								vendorInterfaces[t] = true
							}
						}
					}
				}
				box, ok := ins.(*ssa.MakeInterface)
				if !ok {
					return
				}
				t := types.Unalias(box.X.Type())
				if ptr, ok := t.(*types.Pointer); ok {
					t = types.Unalias(ptr.Elem())
				}
				n, ok := t.(*types.Named)
				if !ok || n.Obj().Pkg() == nil {
					return
				}
				if fromFactory && n.Obj().Pkg() == assembly.Types && types.Implements(box.Type(), iface) {
					receivers[box.X.Type()] = true
				}
				if within(n.Obj().Pkg().Path(), sdk+"/internal/providers") {
					vendorInterfaces[types.Unalias(box.Type())] = true
					path := n.Obj().Pkg().Path()
					result.constructed[path] = append(result.constructed[path], box.X.Type())
				}
			}
			seen := map[*ssa.Function]bool{}
			factoryStep := func(ins ssa.Instruction) { record(ins, true) }
			walkAssembly(prog.FuncValue(f), pkgs[0], seen, factoryStep)
			walkAssembly(pkgs[0].Func("init"), pkgs[0], seen, factoryStep)
			for receiver := range receivers {
				var calls contractSet
				methods := prog.MethodSets.MethodSet(receiver)
				visited := map[*ssa.Function]bool{}
				for m := range iface.Methods() {
					sel := methods.Lookup(m.Pkg(), m.Name())
					walkAssembly(prog.FuncValue(sel.Obj().(*types.Func)), pkgs[0], visited, func(ins ssa.Instruction) {
						record(ins, false)
						if call, ok := ins.(ssa.CallInstruction); ok && call.Common().IsInvoke() {
							t := types.Unalias(call.Common().Value.Type())
							if !slices.Contains(calls, t) {
								calls = append(calls, t)
							}
						}
					})
				}
				used := slices.DeleteFunc(calls, func(t types.Type) bool { return !vendorInterfaces[t] })
				if len(used) > 0 {
					result.alternatives = append(result.alternatives, used)
				}
			}
		}
	}
	return result
}

// Visit the factory and the operations of the facade it constructs. Unrelated
// diagnostic functions cannot contribute supplier contracts.
func walkAssembly(fn *ssa.Function, pkg *ssa.Package, seen map[*ssa.Function]bool, visit func(ssa.Instruction)) {
	if fn == nil || fn.Pkg != pkg || seen[fn] {
		return
	}
	seen[fn] = true
	for _, block := range fn.Blocks {
		for _, ins := range block.Instrs {
			visit(ins)
			if c, ok := ins.(ssa.CallInstruction); ok {
				walkAssembly(c.Common().StaticCallee(), pkg, seen, visit)
			}
		}
	}
}

func factoryCapabilities(f *types.Func, sdk string) []types.Type {
	var result []types.Type
	for r := range f.Signature().Results().Variables() {
		n, ok := types.Unalias(r.Type()).(*types.Named)
		if !ok || n.Obj().Pkg() == nil || n.Obj().Pkg().Path() != sdk || !n.Obj().Exported() {
			continue
		}
		if iface, ok := n.Underlying().(*types.Interface); ok && iface.NumMethods() > 0 {
			result = append(result, n)
		}
	}
	return result
}
