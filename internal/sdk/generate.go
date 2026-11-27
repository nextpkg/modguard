package sdk

import (
	"bytes"
	"fmt"
	"go/format"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

var packageName = regexp.MustCompile(`^[a-z][a-z0-9]*$`)

type scaffold struct {
	Name, Op, Supplier                                       string
	Path, Prefix, SDKRef, FactorySDK, SupplierRef, Qualifier string
}

// Generate scaffolds a new multi- or single-provider SDK under dir.
func Generate(dir, kind, name, op, supplier string) (string, error) {
	if err := generate(dir, kind, name, op, supplier); err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

func validPackage(name string) bool {
	switch name {
	case "cmd", "main", "init", "internal", "vendor", "testdata":
		return false
	}
	return packageName.MatchString(name) && !token.Lookup(name).IsKeyword() && types.Universe.Lookup(name) == nil
}

func importName(name string, used ...string) string {
	for slices.Contains(used, name) {
		name += "pkg"
	}
	return name
}

func generate(dir, kind, name, op, supplier string) error {
	if !validPackage(name) {
		return fmt.Errorf("invalid SDK package name %q", name)
	}
	if !token.IsIdentifier(op) || !token.IsExported(op) {
		return fmt.Errorf("expected an exported Go method name, got %q", op)
	}
	switch kind {
	case "multi":
		if !validPackage(supplier) {
			return fmt.Errorf("expected a supplier package name for multi, got %q", supplier)
		}
	case "single":
		if supplier != "" {
			return fmt.Errorf("single SDK takes no provider option")
		}
	default:
		return fmt.Errorf("unknown SDK kind %q (use multi or single)", kind)
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	host, err := loadSDKModule(dir)
	if err != nil {
		return err
	}
	data := scaffold{Name: name, Op: op, Supplier: supplier, Prefix: strings.ToUpper(name),
		Path: host.importPath + "/" + name, SDKRef: name, SupplierRef: supplier}
	if kind == "multi" {
		data.SDKRef = importName(name, "context", "ctx", "input", "err")
		data.FactorySDK = importName(name, "cfg")
		data.SupplierRef = importName(supplier, "cfg", data.FactorySDK)
		data.Qualifier = data.SDKRef + "."
	}

	// Render beside sdks so concurrent checks cannot see a partial SDK. Rename
	// publishes the complete directory or reports an existing destination.
	staging, err := os.MkdirTemp(filepath.Dir(dir), ".sdk-new-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	tree := filepath.Join(staging, name)
	for _, rule := range append(layoutFor(kind), fileRule{Path: "testdata/consumer_test.go"}) {
		path := rule.file(supplier)
		var buf bytes.Buffer
		if err := sources.ExecuteTemplate(&buf, filepath.Base(path)+".tmpl", data); err != nil {
			return err
		}
		code, err := format.Source(buf.Bytes())
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		target := filepath.Join(tree, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(target, code, 0644); err != nil {
			return err
		}
	}
	target := filepath.Join(dir, name)
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("SDK destination already exists: %s", target)
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(tree, target)
}
