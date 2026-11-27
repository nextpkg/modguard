package sdk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}
}

// Both SDKs deliberately omit *_test.go. The third-party module is outside
// the repository so this fixture can exercise an independent dependency offline.
func fixture(t *testing.T) string {
	t.Helper()
	t.Setenv("GOWORK", "off")
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOFLAGS", "")
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	files := map[string]string{
		".git/HEAD":                                     "ref: refs/heads/main\n",
		"go.mod":                                        "module host\n\ngo 1.26\nrequire private.example/cfg v0.0.0\nreplace private.example/cfg => ../external\nreplace private.example/local => ./proxy\nreplace private.example/remote => github.com/example/repo/proxy v0.1.0\n",
		"proxy/go.mod":                                  "module github.com/example/repo/proxy\n\ngo 1.26\n",
		"sdks/single/config.go":                         "package single\nimport \"private.example/cfg\"\ntype Config struct { Value cfg.Value }\n",
		"sdks/single/types.go":                          "package single\ntype Result string\n",
		"sdks/single/errors.go":                         "package single\n",
		"sdks/single/client.go":                         "package single\nimport \"host/sdks/single/internal/impl\"\ntype Client struct { inner *impl.Token }\nfunc New(Config) *Client { return &Client{} }\nfunc (*Client) Read() Result { return \"\" }\n",
		"sdks/single/internal/impl/client.go":           "package impl\ntype Token struct{}\nfunc New() *Token { return nil }\nfunc Forward[T any]() {}\n",
		"sdks/multi/config.go":                          "package multi\ntype Config struct{}\n",
		"sdks/multi/types.go":                           "package multi\n",
		"sdks/multi/errors.go":                          "package multi\n",
		"sdks/multi/provider.go":                        "package multi\ntype Provider interface { Read() string }\n",
		"sdks/multi/provider/assemble.go":               "package provider\nimport (\"host/sdks/multi\"; \"host/sdks/multi/internal/providers/local\")\nfunc New(multi.Config) multi.Provider { return local.Client{} }\n",
		"sdks/multi/internal/providers/local/client.go": "package local\ntype Client struct{}\nfunc (Client) Read() string { return \"\" }\n",
	}
	for path, contents := range files {
		writeFixture(t, repo, path, contents)
	}
	writeFixture(t, parent, "external/go.mod", "module private.example/cfg\n\ngo 1.26\n")
	writeFixture(t, parent, "external/value.go", "package cfg\ntype Value string\n")
	return repo
}

func TestBothLayoutsWithoutTestFiles(t *testing.T) {
	root := fixture(t)
	count, problems, err := check(filepath.Join(root, "sdks"))
	if err != nil || count != 2 || len(problems) != 0 {
		t.Fatalf("count=%d problems=%v err=%v", count, problems, err)
	}
}

func TestTypesFileDependsOnPublicDataForSinglePlatform(t *testing.T) {
	root := fixture(t)
	sdks := filepath.Join(root, "sdks")
	if err := os.Remove(filepath.Join(sdks, "single", "types.go")); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, "sdks/single/client.go", `package single
type Client struct{}
func New(Config) *Client { return &Client{} }
func (*Client) Read() (string, error) { return "", nil }
`)
	writeFixture(t, root, "sdks/single/errors.go", "package single\ntype Issue int\nfunc (*Issue) Error() string { return \"issue\" }\n")
	if _, problems, err := check(sdks); err != nil || len(problems) != 0 {
		t.Fatalf("SDK with built-in results: problems=%v err=%v", problems, err)
	}
	writeFixture(t, root, "sdks/single/lookup.go", "package single\ntype Item string\nfunc (*Client) Lookup() Item { return \"\" }\n")
	if err := os.Remove(filepath.Join(sdks, "multi", "types.go")); err != nil {
		t.Fatal(err)
	}
	verifyProblems(t, root, "sdks/single/types.go [layout]", "sdks/multi/types.go [layout]")
}

func TestLayoutAndRepositoryImports(t *testing.T) {
	root := fixture(t)
	if err := os.Remove(filepath.Join(root, "sdks", "single", "errors.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "sdks", "multi", "internal", "providers", "local", "client.go")); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, "sdks/single/private/client.go", "package private\n")
	writeFixture(t, root, "sdks/single/internal/impl/guard_test.go", `//go:build unusedtag

package impl
import (
	_ "host/services"
	_ "host/sdks/multi"
	_ "github.com/example/repo/proxy/auth"
	_ "github.com/example/repo/other"
	_ "private.example/local"
    _ "private.example/remote"
)
`)
	verifyProblems(t, root,
		"sdks/single/errors.go [layout]", "sdks/single/private [layout]",
		"sdks/multi/internal/providers/local/client.go [layout]",
		"another repository package: host/services",
		"another repository package: host/sdks/multi",
		"another repository package: github.com/example/repo/proxy/auth",
		"another repository package: github.com/example/repo/other",
		"another repository package: private.example/local", "another repository package: private.example/remote")
}

func TestFactorySignatures(t *testing.T) {
	root := fixture(t)
	writeFixture(t, root, "sdks/single/client.go", "package single\ntype Client struct{}\nfunc New() int { return 1 }\nfunc (Client) Read() {}\n")
	writeFixture(t, root, "sdks/multi/provider/assemble.go", "package provider\nfunc Fake() int { return 1 }\n")
	verifyProblems(t, root, "expected New to return a Client", "expected a factory returning a public SDK capability interface")
}

func TestMultiProviderFactoriesBelongInAssembly(t *testing.T) {
	root := fixture(t)
	writeFixture(t, root, "sdks/multi/provider.go", `package multi
type Provider interface { Read() string }
type FullProvider interface { Provider; Close() error }
`)
	writeFixture(t, root, "sdks/multi/internal/providers/local/close.go", "package local\nfunc (Client) Close() error { return nil }\n")
	writeFixture(t, root, "sdks/multi/provider/assemble.go", `package provider
import ("host/sdks/multi"; "host/sdks/multi/internal/providers/local")
func New(multi.Config) multi.FullProvider { return local.Client{} }
`)
	writeFixture(t, root, "sdks/multi/default.go", `package multi
type local struct{}
func (*local) Read() string { return "" }
type Alias = Provider
func NewDefault() (Alias, error) { return &local{}, nil }
func FromConfig(Config) *local { return &local{} }
var Open = FromConfig
type Metadata interface { Name() string }
func Describe() Metadata { return nil }
func DefaultConfig() Config { return Config{} }
`)
	_, problems, err := check(filepath.Join(root, "sdks"))
	if err != nil || len(problems) != 3 {
		t.Fatalf("problems=%v err=%v", problems, err)
	}
	all := strings.Join(problems, "\n")
	for _, name := range []string{"NewDefault", "FromConfig", "Open"} {
		if !strings.Contains(all, "multi-provider root exposes "+name+"; construct clients in provider/") {
			t.Errorf("missing diagnostic for %s in %v", name, problems)
		}
	}
}

func TestPublicAPISemantics(t *testing.T) {
	root := fixture(t)
	writeFixture(t, root, "sdks/single/leak.go", `package single
import i "host/sdks/single/internal/impl"
type hidden = i.Token
type Alias = hidden
type Copied i.Token
type Container[T any] struct { private T }
type ResultBox struct { Tokens Container[*hidden] }
var Default = i.New()
var Call = i.Forward[int]
func (*Client) Leak() <-chan *hidden { return nil }
type Constraint[T interface{ *hidden }] struct{}
type Readable interface { Items() map[string]*hidden }
`)
	verifyProblems(t, root, "Alias exposes internal", "Copied re-exports", "ResultBox exposes internal", "Default exposes internal", "Call re-exports", "Client exposes internal", "Constraint exposes internal", "Readable exposes internal")
}

func verifyProblems(t *testing.T, root string, want ...string) {
	t.Helper()
	_, problems, err := check(filepath.Join(root, "sdks"))
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(problems, "\n")
	for _, substring := range want {
		if !strings.Contains(all, substring) {
			t.Errorf("missing %q in:\n%s", substring, all)
		}
	}
}

func TestUnwiredSupplierMustTypeCheck(t *testing.T) {
	root := fixture(t)
	writeFixture(t, root, "sdks/multi/internal/providers/second/client.go", `package second
type Client struct{}
func (Client) Read() string { return missingImplementation() }
`)
	verifyProblems(t, root, "undefined: missingImplementation")
}

func TestInternalFunctionForwardedThroughPrivateVariables(t *testing.T) {
	root := fixture(t)
	writeFixture(t, root, "sdks/single/binding.go", `package single
import "host/sdks/single/internal/impl"
var hook = impl.Forward[int]
var intermediary = hook
`)
	writeFixture(t, root, "sdks/single/export.go", "package single\nvar PublicHook = intermediary\n")
	verifyProblems(t, root, "PublicHook re-exports host/sdks/single/internal/impl.Forward")
}

func TestActiveConfigAndMultiProviderBoundaries(t *testing.T) {
	root := fixture(t)
	writeFixture(t, root, "sdks/multi/direct.go", "package multi\ntype Client struct{}\nfunc New(Config) *Client { return &Client{} }\n")
	writeFixture(t, root, "sdks/multi/internal/providers/second/client.go", "package second\ntype Client struct{}\nfunc (*Client) Name() string { return \"second\" }\n")
	writeFixture(t, root, "sdks/single/config.go", "//go:build specialcfg\n\npackage single\ntype Config struct{}\n")
	writeFixture(t, root, "sdks/single/config_default.go", "//go:build !specialcfg\n\npackage single\ntype Config int\n")
	verifyProblems(t, root, "direct.go", "multi-provider root", "second", "does not implement", "active Config must be a struct")
}

func TestAssignedInternalFunctionsAndHiddenState(t *testing.T) {
	root := fixture(t)
	writeFixture(t, root, "sdks/single/bind.go", `package single
import "host/sdks/single/internal/impl"
type hidden = impl.Token
type Box struct { hidden }
var Hook func()
var privateHook func()
func init(){ privateHook=impl.Forward[int]; Hook=privateHook }
`)
	_, problems, err := check(filepath.Join(root, "sdks"))
	if err != nil || len(problems) != 1 || !strings.Contains(problems[0], "Hook re-exports") {
		t.Fatalf("problems=%v err=%v", problems, err)
	}
}
func TestDiagnosticInterfaceIsNotASupplierContract(t *testing.T) {
	root := fixture(t)
	writeFixture(t, root, "sdks/multi/internal/providers/local/name.go", "package local\nfunc(Client) Name()string{return \"local\"}\n")
	writeFixture(t, root, "sdks/multi/provider/name.go", `package provider
import "host/sdks/multi/internal/providers/local"
type named interface { Name()string }
func VendorName()string{var n named = local.Client{}; return n.Name()}
type unusedFacade struct{ n named }
func (f unusedFacade) Read()string{return f.n.Name()}
`)
	writeFixture(t, root, "sdks/multi/internal/providers/other/client.go", "package other\ntype Client struct{}\nfunc(Client) Name() string{return \"other\"}\n")
	verifyProblems(t, root, "does not implement")
}

func TestCallbackBindingsRespectPublicFields(t *testing.T) {
	root := fixture(t)
	writeFixture(t, root, "sdks/single/callbacks.go", `package single
import "host/sdks/single/internal/impl"
type callbacks struct { Login func(); hidden func() }
var Hooks = callbacks{Login: impl.Forward[int]}
var Assigned callbacks
var PrivateState = callbacks{hidden: impl.Forward[int]}
func init(){Assigned.Login = impl.Forward[int]}
`)
	_, problems, err := check(filepath.Join(root, "sdks"))
	all := strings.Join(problems, "\n")
	if err != nil || len(problems) != 2 || !strings.Contains(all, "Hooks re-exports") || !strings.Contains(all, "Assigned re-exports") {
		t.Fatalf("problems=%v err=%v", problems, err)
	}
}

func TestUnwiredSupplierMustReturnItsImplementation(t *testing.T) {
	root := fixture(t)
	writeFixture(t, root, "sdks/multi/internal/providers/spare/client.go", `package spare
type Client struct{}
func New()*Client{return &Client{}}
type unused struct{}
func (unused) Read()string{return ""}
`)
	verifyProblems(t, root, "spare [layout] supplier does not implement")
}

func TestSupplierEntryPointsMatchTheAssembly(t *testing.T) {
	root := fixture(t)
	writeFixture(t, root, "sdks/multi/internal/model/backend.go", "package model\ntype Backend interface{Lookup() string}\n")
	writeFixture(t, root, "sdks/multi/internal/providers/local/client.go", `package local
import "host/sdks/multi/internal/model"
type Client struct{}
func New() model.Backend {return Client{}}
func (Client) Lookup()string{return "ok"}
`)
	writeFixture(t, root, "sdks/multi/provider/assemble.go", `package provider
import ("host/sdks/multi"; "host/sdks/multi/internal/model"; "host/sdks/multi/internal/providers/local")
type router struct{ backend model.Backend }
func(r router)Read()string{return r.backend.Lookup()}
func New(multi.Config) multi.Provider {return router{local.New()}}
`)
	writeFixture(t, root, "sdks/multi/internal/providers/spare/client.go", `package spare
type Client struct{}
func New() *Client {return &Client{}}
type unrelated struct{}
func (unrelated) Lookup()string{return ""}
`)
	_, problems, err := check(filepath.Join(root, "sdks"))
	if err != nil || len(problems) != 1 || !strings.Contains(problems[0], "/spare") || !strings.Contains(problems[0], "does not implement") {
		t.Fatalf("problems=%v err=%v", problems, err)
	}
}

func TestPromotedFieldsRespectOuterMethods(t *testing.T) {
	root := fixture(t)
	writeFixture(t, root, "sdks/single/fields.go", `package single
import "host/sdks/single/internal/impl"
type embedded struct { Token impl.Token }
type WithValueMethod struct { embedded }
func (WithValueMethod) Token() string { return "" }
type WithPointerMethod struct { *embedded }
func (*WithPointerMethod) Token() string { return "" }
type WithVisibleField struct { embedded }
`)
	_, problems, err := check(filepath.Join(root, "sdks"))
	if err != nil || len(problems) != 1 || !strings.Contains(problems[0], "WithVisibleField exposes internal type") {
		t.Fatalf("problems=%v err=%v", problems, err)
	}
}

func TestCheckPackageNames(t *testing.T) {
	root := fixture(t)
	dir := filepath.Join(root, "sdks")
	// External and in-package tests follow the Go conventions. Test fixtures,
	// toolkit commands, and private helpers choose their own package names.
	for name, pkg := range map[string]string{
		"single/local_test.go":                             "single",
		"multi/public_test.go":                             "multi_test",
		"multi/provider/public_test.go":                    "provider_test",
		"multi/internal/providers/local/public_test.go":    "local_test",
		"multi/internal/providers/local/io-helpers/doc.go": "helpers",
		"single/internal/private-utils/doc.go":             "utility",
		"single/testdata/consumer_test.go":                 "consumer_test",
	} {
		writeFixture(t, dir, name, "package "+pkg+"\n")
	}
	if _, problems, err := check(dir); err != nil || len(problems) > 0 {
		t.Fatalf("valid packages returned %v, %v", problems, err)
	}
	// cmd/ is forbidden inside an SDK collection.
	writeFixture(t, dir, "cmd/tool/main.go", "package main\n")
	for name, pkg := range map[string]string{
		"single/conditional.go":                              "other",
		"multi/provider/conditional.go":                      "factory",
		"multi/internal/providers/local/conditional.go":      "supplier",
		"multi/internal/providers/local/conditional_test.go": "different_test",
		"multi/nontest.go":                                   "multi_test",
	} {
		writeFixture(t, dir, name, "//go:build modguard_unselected\n\npackage "+pkg+"\n")
	}
	for _, name := range []string{"BadSDK", "string"} {
		for _, file := range []string{"config", "types", "errors", "client"} {
			source := "package " + name + "\n"
			switch file {
			case "config":
				source += "type Config struct{}\n"
			case "client":
				source += "type Client struct{}\nfunc New(Config)*Client{return &Client{}}\nfunc(Client) Read()string{return \"\"}\n"
			}
			writeFixture(t, dir, name+"/"+file+".go", source)
		}
	}
	for _, name := range []string{"BadSupplier", "bool"} {
		writeFixture(t, dir, "multi/internal/providers/"+name+"/client.go", "package "+name+"\ntype Client struct{}\nfunc(Client) Read()string{return \"\"}\n")
	}
	verifyProblems(t, root, "invalid SDK package name \"BadSDK\"", "invalid SDK package name \"string\"",
		"invalid supplier package name \"BadSupplier\"", "invalid supplier package name \"bool\"",
		"SDK collection directory must not contain cmd/",
		"single/conditional.go:3 [layout] expected package single",
		"multi/provider/conditional.go:3 [layout] expected package provider",
		"multi/internal/providers/local/conditional.go:3 [layout] expected package local",
		"multi/internal/providers/local/conditional_test.go:3 [layout] expected package local or local_test",
		"multi/nontest.go:3 [layout] expected package multi")
}
