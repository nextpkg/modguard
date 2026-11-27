// Command modguard enforces Go module boundaries, architectural rules,
// standalone exportability, and compliant module/SDK scaffolding without YAML config.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"github.com/nextpkg/modguard"
	"github.com/nextpkg/modguard/internal/modcheck"
	"github.com/nextpkg/modguard/internal/sdk"
	"github.com/nextpkg/modguard/internal/skillmgr"
)

// version can be overridden at build time via -ldflags "-X main.version=v0.1.0".
var version = ""

const usageText = `modguard — Zero-config Go module boundary guard and scaffold tool

Usage:
  modguard check [flags] [dir ...]          Check one or more standalone Go modules (default: .)
  modguard new [flags]                      Scaffold a standalone Go service or library module
  modguard sdk check [flags] [dir]          Check an SDK collection and standalone consumer tests (default: .)
  modguard sdk new [flags] [dir]            Scaffold a multi- or single-provider Go SDK (default: .)
  modguard skill install [flags] [dir]      Install modguard skill into a target repo (default: .)
  modguard skill update [flags] [dir]       Update an existing modguard skill installation (default: .)
  modguard skill remove [flags] [dir]       Remove modguard skill from a target repo (default: .)
  modguard version                          Print modguard version
  modguard help [command]                   Show help for modguard or a subcommand

Levels (--level for check):
  relaxed    Cross-module imports, relative imports, symlinks, local replace
  standard   relaxed + no pkg/ dir, package naming, init() side effects, internal API leaks
  strict     standard + out-of-repo GOWORK=off standalone export tests

All list/rule flags are composable: repeat a flag or pass comma-separated values.
Run "modguard <command> -h" for command-specific flags.
`

const sdkUsageText = `Usage:
  modguard sdk check [flags] [dir]     Check an SDK collection and standalone consumer tests (default: .)
  modguard sdk new [flags] [dir]       Scaffold a multi- or single-provider Go SDK (default: .)

Run "modguard sdk <subcommand> -h" for flags.
`

const skillUsageText = `Usage:
  modguard skill install [flags] [dir]   Install modguard skill into <dir>/.agents/skills/modguard (default: .)
  modguard skill update [flags] [dir]    Update an existing modguard skill installation (default: .)
  modguard skill remove [flags] [dir]    Remove an installed modguard skill (default: .)

Run "modguard skill <subcommand> -h" for flags.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	var err error
	switch args[0] {
	case "check":
		err = runCheck(args[1:], stdout, stderr)
	case "new":
		err = runNew(args[1:], stdout, stderr)
	case "sdk":
		err = runSDK(args[1:], stdout, stderr)
	case "skill":
		err = runSkill(args[1:], stdout, stderr)
	case "version", "-v", "--version":
		fmt.Fprintf(stdout, "modguard %s\n", buildVersion())
		return 0
	case "help", "-h", "--help":
		return runHelp(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usageText)
		return 2
	}
	if err == nil {
		return 0
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	var ue usageError
	if errors.As(err, &ue) {
		fmt.Fprintln(stderr, ue.Error())
		return 2
	}
	fmt.Fprintln(stderr, err)
	return 1
}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func runHelp(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, usageText)
		return 0
	}
	switch args[0] {
	case "check":
		_ = runCheck([]string{"-h"}, stdout, stdout)
		return 0
	case "new":
		_ = runNew([]string{"-h"}, stdout, stdout)
		return 0
	case "sdk":
		if len(args) > 1 {
			_ = runSDK([]string{args[1], "-h"}, stdout, stdout)
			return 0
		}
		fmt.Fprint(stdout, sdkUsageText)
		return 0
	case "skill":
		if len(args) > 1 {
			_ = runSkill([]string{args[1], "-h"}, stdout, stdout)
			return 0
		}
		fmt.Fprint(stdout, skillUsageText)
		return 0
	case "version":
		fmt.Fprintln(stdout, "Usage: modguard version\n\nPrint the modguard binary version.")
		return 0
	default:
		fmt.Fprintf(stderr, "unknown help topic %q\n\n%s", args[0], usageText)
		return 2
	}
}

func buildVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "devel"
}

func runCheck(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("modguard check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: modguard check [flags] [dir ...]\n\nCheck one or more standalone Go modules (default: .).\nFlags may appear before or after directory arguments.\nList flags may be repeated or comma-separated.\n\nFlags:\n")
		fs.PrintDefaults()
	}

	levelStr := fs.String("level", "standard", "strictness level: relaxed, standard, or strict")
	var allowImport, forbidImport, onlyImportIn, forbidLayer, forbidCall, allowEnv, allowInternalIn, excludeDirTest stringList
	fs.Var(&allowImport, "allow-import", "repo module prefix allowed to be imported (repeatable or CSV)")
	fs.Var(&forbidImport, "forbid-import", "extra forbidden import prefix in non-test files (repeatable or CSV)")
	fs.Var(&onlyImportIn, "only-import-in", "restrict import prefix to dir: pkgPrefix=dirPrefix (repeatable or CSV)")
	fs.Var(&forbidLayer, "forbid-layer", "forbid internal layer import: fromDir:toDir (repeatable or CSV)")
	fs.Var(&forbidCall, "forbid-call", "forbidden direct selector function/method name (repeatable or CSV)")
	fs.Var(&allowEnv, "allow-env", "allowed os.Getenv/os.LookupEnv keys (repeatable or CSV)")
	forbidEnv := fs.Bool("forbid-env", false, "forbid all os.Getenv/os.LookupEnv/os.Environ calls in non-test files")
	fs.Var(&allowInternalIn, "allow-internal-in", "module-local dir prefix exempted from api/no-internal-leak (repeatable or CSV)")
	fs.Var(&excludeDirTest, "exclude-dir-test", "extra standalone test after removing dir: dir:pkgPattern (repeatable or CSV)")
	skipStandalone := fs.Bool("skip-standalone", false, "skip out-of-repo standalone export tests at strict level")
	withTools := fs.Bool("with-tools", false, "also run gofmt and go vet")
	allTools := fs.Bool("all-tools", false, "run gofmt, go vet, gci, staticcheck, and deadcode using verified pinned versions")
	security := fs.Bool("security", false, "run govulncheck and gosec using verified pinned versions")
	gci := fs.String("gci", "", "run gci import-order check (auto, binary name, or go run pkg@version)")
	staticcheck := fs.String("staticcheck", "", "run staticcheck -checks=all ./... (auto, binary name, or go run pkg@version)")
	deadcode := fs.String("deadcode", "", "run deadcode ./... (auto, binary name, or go run pkg@version)")
	govulncheck := fs.String("govulncheck", "", "run govulncheck ./... (auto, binary name, or go run pkg@version)")
	gosec := fs.String("gosec", "", "run gosec -quiet -exclude=G104 ./... (auto, binary name, or go run pkg@version)")

	dirs, err := parseFlagsInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(dirs) == 0 {
		dirs = []string{"."}
	}
	level, err := modcheck.ParseLevel(*levelStr)
	if err != nil {
		return usageError{err.Error()}
	}
	onlyIn, err := parsePairMap(onlyImportIn, "=")
	if err != nil {
		return usageError{fmt.Sprintf("invalid --only-import-in: %v", err)}
	}
	layers, err := parsePairs(forbidLayer, ":")
	if err != nil {
		return usageError{fmt.Sprintf("invalid --forbid-layer: %v", err)}
	}
	excludeTests, err := parsePairs(excludeDirTest, ":")
	if err != nil {
		return usageError{fmt.Sprintf("invalid --exclude-dir-test: %v", err)}
	}

	var failed []string
	for _, dir := range dirs {
		opts := modcheck.Options{
			Dir:             dir,
			Level:           level,
			AllowImports:    allowImport,
			ForbidImports:   forbidImport,
			OnlyImportIn:    onlyIn,
			ForbidLayers:    layers,
			ForbidCalls:     forbidCall,
			AllowEnv:        allowEnv,
			ForbidEnv:       *forbidEnv,
			AllowInternalIn: allowInternalIn,
			ExcludeDirTests: excludeTests,
			SkipStandalone:  *skipStandalone,
			WithTools:       *withTools,
			AllTools:        *allTools,
			Security:        *security,
			GCISpec:         strings.TrimSpace(*gci),
			StaticcheckSpec: strings.TrimSpace(*staticcheck),
			DeadcodeSpec:    strings.TrimSpace(*deadcode),
			GovulncheckSpec: strings.TrimSpace(*govulncheck),
			GosecSpec:       strings.TrimSpace(*gosec),
		}
		result, err := modcheck.Check(opts)
		if err != nil {
			return err
		}
		for _, p := range result.Problems {
			if len(dirs) > 1 {
				fmt.Fprintf(stderr, "%s: %s\n", dir, p)
			} else {
				fmt.Fprintln(stderr, p)
			}
		}
		if len(result.Problems) > 0 {
			failed = append(failed, fmt.Sprintf("%s (%d)", result.ModulePath, len(result.Problems)))
		} else {
			fmt.Fprintf(stdout, "modguard check: %s (%s) passed\n", result.ModulePath, level)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("modguard check failed: %s", strings.Join(failed, ", "))
	}
	return nil
}

func runNew(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("modguard new", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: modguard new [flags]\n\nScaffold a standalone Go service or library module that passes strict checks.\n\nFlags:\n")
		fs.PrintDefaults()
	}

	dir := fs.String("dir", ".", "parent directory for the new module")
	kind := fs.String("kind", "service", "module kind: service or lib")
	name := fs.String("name", "", "module directory name (lowercase letters, digits, hyphens)")
	modPath := fs.String("module", "", "Go module path (e.g. github.com/org/myservice)")
	goVer := fs.String("go", "1.26.0", "Go version for go.mod")

	pos, err := parseFlagsInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usageError{fmt.Sprintf("unexpected argument %q", pos[0])}
	}
	target, err := modcheck.GenerateModule(modcheck.ScaffoldOptions{
		ParentDir:  *dir,
		Kind:       *kind,
		Name:       *name,
		ModulePath: *modPath,
		GoVersion:  *goVer,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "created standalone %s module %s\n", *kind, target)
	return nil
}

func runSDK(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, sdkUsageText)
		return usageError{"missing sdk subcommand (use check or new)"}
	}
	switch args[0] {
	case "check":
		return runSDKCheck(args[1:], stdout, stderr)
	case "new":
		return runSDKNew(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, sdkUsageText)
		return nil
	default:
		fmt.Fprintf(stderr, "unknown sdk subcommand %q\n\n%s", args[0], sdkUsageText)
		return usageError{fmt.Sprintf("unknown sdk subcommand %q", args[0])}
	}
}

func runSDKCheck(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("modguard sdk check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: modguard sdk check [flags] [dir]\n\nValidate SDK collection layout, API boundaries, supplier contracts, and standalone consumer tests (default: .).\n\nFlags:\n")
		fs.PrintDefaults()
	}

	skipStandalone := fs.Bool("skip-standalone", false, "skip out-of-repo standalone SDK and consumer tests")
	pos, err := parseFlagsInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usageError{fmt.Sprintf("expected at most one SDK directory, got %d arguments", len(pos))}
	}
	dir := "."
	if len(pos) == 1 {
		dir = pos[0]
	}
	return sdk.Check(dir, !*skipStandalone, stdout, stderr)
}

func runSDKNew(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("modguard sdk new", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: modguard sdk new [flags] [dir]\n\nScaffold a multi- or single-provider Go SDK under dir (default: .).\n\nFlags:\n")
		fs.PrintDefaults()
	}

	kind := fs.String("kind", "multi", "SDK kind: multi or single")
	name := fs.String("name", "", "new SDK package name")
	op := fs.String("op", "", "exported capability method name")
	provider := fs.String("provider", "", "initial supplier package for a multi-provider SDK")
	pos, err := parseFlagsInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usageError{fmt.Sprintf("expected at most one SDK directory, got %d arguments", len(pos))}
	}
	dir := "."
	if len(pos) == 1 {
		dir = pos[0]
	}
	target, err := sdk.Generate(dir, *kind, *name, *op, *provider)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "created %s\n", target)
	return nil
}

func runSkill(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, skillUsageText)
		return usageError{"missing skill subcommand (use install, update, or remove)"}
	}
	switch args[0] {
	case "install", "update", "remove":
		return runSkillAction(args[0], args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, skillUsageText)
		return nil
	default:
		fmt.Fprintf(stderr, "unknown skill subcommand %q\n\n%s", args[0], skillUsageText)
		return usageError{fmt.Sprintf("unknown skill subcommand %q", args[0])}
	}
}

func runSkillAction(action string, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("modguard skill "+action, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: modguard skill %s [flags] [dir]\n\nManage the bundled modguard agent skill in target repository [dir] (default: .).\n\nFlags:\n", action)
		fs.PrintDefaults()
	}

	dest := fs.String("dest", skillmgr.DefaultSkillsDir, "skills parent directory (relative to dir, or absolute)")
	var force *bool
	if action != "remove" {
		force = fs.Bool("force", false, "overwrite on install, or install if missing on update")
	}
	pos, err := parseFlagsInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usageError{fmt.Sprintf("expected at most one target directory, got %d arguments", len(pos))}
	}
	dir := "."
	if len(pos) == 1 {
		dir = pos[0]
	}
	opts := skillmgr.Options{
		TargetDir: dir,
		DestDir:   *dest,
		Force:     force != nil && *force,
	}
	switch action {
	case "install":
		target, err := skillmgr.Install(modguard.SkillFS, opts)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "installed modguard skill to %s\n", target)
	case "update":
		target, err := skillmgr.Update(modguard.SkillFS, opts)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "updated modguard skill at %s\n", target)
	case "remove":
		target, err := skillmgr.Remove(opts)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "removed modguard skill from %s\n", target)
	}
	return nil
}

// stringList is a composable flag.Value that supports both repeating a flag
// (--allow-env A --allow-env B) and comma-separated values (--allow-env A,B).
type stringList []string

func (s *stringList) String() string {
	return strings.Join(*s, ",")
}

func (s *stringList) Set(raw string) error {
	for _, part := range strings.Split(raw, ",") {
		p := strings.TrimSpace(part)
		if p != "" {
			*s = append(*s, p)
		}
	}
	return nil
}

// parseFlagsInterleaved parses flags anywhere in args (before or after positional
// arguments), stopping flag interpretation after "--".
func parseFlagsInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var flagArgs, posArgs []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			posArgs = append(posArgs, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			posArgs = append(posArgs, arg)
			continue
		}
		flagArgs = append(flagArgs, arg)
		trimmed := strings.TrimLeft(arg, "-")
		if strings.Contains(trimmed, "=") || trimmed == "h" || trimmed == "help" {
			continue
		}
		if f := fs.Lookup(trimmed); f != nil {
			if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
				continue
			}
			if i+1 < len(args) {
				i++
				flagArgs = append(flagArgs, args[i])
			}
		}
	}
	if err := fs.Parse(flagArgs); err != nil {
		return nil, err
	}
	posArgs = append(posArgs, fs.Args()...)
	return posArgs, nil
}

func parsePairMap(items []string, sep string) (map[string]string, error) {
	if len(items) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(items))
	for _, item := range items {
		k, v, ok := strings.Cut(item, sep)
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" || v == "" {
			return nil, fmt.Errorf("expected key%svalue, got %q", sep, item)
		}
		out[k] = v
	}
	return out, nil
}

func parsePairs(items []string, sep string) ([]modcheck.Pair, error) {
	if len(items) == 0 {
		return nil, nil
	}
	out := make([]modcheck.Pair, 0, len(items))
	for _, item := range items {
		a, b, ok := strings.Cut(item, sep)
		a, b = strings.TrimSpace(a), strings.TrimSpace(b)
		if !ok || a == "" || b == "" {
			return nil, fmt.Errorf("expected left%sright, got %q", sep, item)
		}
		out = append(out, modcheck.Pair{Left: a, Right: b})
	}
	return out, nil
}
