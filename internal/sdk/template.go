package sdk

import (
	"embed"
	"encoding/json"
	"path"
	"slices"
	"strings"
	"text/template"
)

//go:embed templates
var templateFiles embed.FS

var sources = template.Must(template.ParseFS(templateFiles, "templates/*.go.tmpl"))
var layouts = readLayouts()

type fileRule struct {
	Role string `json:"role"`
	Path string `json:"path"`
}

func readLayouts() map[string][]fileRule {
	data, err := templateFiles.ReadFile("templates/layout.json")
	if err != nil {
		panic(err)
	}
	var rules map[string][]fileRule
	if err := json.Unmarshal(data, &rules); err != nil {
		panic(err)
	}
	return rules
}

func layoutFor(kind string) []fileRule {
	return slices.Concat(layouts["common"], layouts[kind])
}

func (r fileRule) file(supplier string) string {
	return strings.ReplaceAll(r.Path, "{provider}", supplier)
}

// A factory may live in any file in its package. Each supplier has its own
// instance of the template directory. Those directories identify the layout.
func (r fileRule) directory() string {
	if before, _, ok := strings.Cut(r.Path, "/{provider}/"); ok {
		return before
	}
	return path.Dir(r.Path)
}
