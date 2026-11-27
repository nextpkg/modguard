package sdk

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"
)

type sdkModule struct {
	root       string
	importPath string // Import path of the directory containing all SDKs.
	manifest   *modfile.File
}

func loadSDKModule(dir string) (sdkModule, error) {
	name, err := runGo(dir, "env", "GOMOD")
	if err != nil {
		return sdkModule{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" || name == os.DevNull {
		return sdkModule{}, fmt.Errorf("%s is outside a Go module", dir)
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return sdkModule{}, err
	}
	manifest, err := modfile.Parse(name, data, nil)
	if err != nil {
		return sdkModule{}, err
	}
	if manifest.Module == nil {
		return sdkModule{}, fmt.Errorf("%s: missing module declaration", name)
	}
	root := filepath.Dir(name)
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return sdkModule{}, err
	}
	return sdkModule{root: root, importPath: path.Join(manifest.Module.Mod.Path, filepath.ToSlash(rel)), manifest: manifest}, nil
}
