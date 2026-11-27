package sdk

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"
)

// Run each SDK with only its own sources and the external module versions used
// by its packages and tests. A local replace or workspace cannot supply code.
func checkStandaloneSDKs(dir string, stdout io.Writer) error {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	host, err := loadSDKModule(dir)
	if err != nil {
		return err
	}
	tempRoot, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return err
	}
	if rel, err := filepath.Rel(repositoryRoot(host.root), tempRoot); err == nil && (rel == "." || filepath.IsLocal(rel)) {
		return errors.New("standalone checks need a temporary directory outside the repository")
	}
	target, err := runGo(dir, "env", "GOOS", "GOARCH", "GOHOSTOS", "GOHOSTARCH")
	if err != nil {
		return err
	}
	platform := strings.Fields(target)
	if len(platform) != 4 {
		return fmt.Errorf("unexpected Go platform %q", target)
	}
	cross := platform[0] != platform[2] || platform[1] != platform[3]
	if cross {
		fmt.Fprintf(stdout, "modguard sdk check: %s/%s target, compile and link only (host %s/%s)\n", platform[0], platform[1], platform[2], platform[3])
	}
	var failures []error
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		name := entry.Name()
		if err := standaloneSDK(filepath.Join(dir, name), host, cross); err != nil {
			failures = append(failures, fmt.Errorf("%s [standalone] %w", name, err))
		}
	}
	return errors.Join(failures...)
}

func standaloneSDK(src string, host sdkModule, cross bool) error {
	module := host.importPath + "/" + filepath.Base(src)
	tmp, err := os.MkdirTemp("", "modguard-sdk-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	dest := filepath.Join(tmp, "sdk")
	if err := os.CopyFS(dest, os.DirFS(src)); err != nil {
		return err
	}
	// Preserve the shared lockfile exactly. Give the copied directory its
	// original import path, without adding a dependency on the host checkout.
	raw, err := os.ReadFile(filepath.Join(host.root, "go.mod"))
	if err != nil {
		return err
	}
	manifest, err := modfile.Parse("go.mod", raw, nil)
	if err != nil {
		return err
	}
	if err := manifest.AddModuleStmt(module); err != nil {
		return err
	}
	for _, r := range manifest.Replace {
		if r.New.Version == "" {
			return fmt.Errorf("shared go.mod depends on local replacement %s", r.New.Path)
		}
	}
	lock, err := manifest.Format()
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dest, "go.mod"), lock, 0600); err != nil {
		return err
	}
	sum, err := os.ReadFile(filepath.Join(host.root, "go.sum"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(sum) != 0 {
		if err := os.WriteFile(filepath.Join(dest, "go.sum"), sum, 0600); err != nil {
			return err
		}
	}
	consumer := filepath.Join(tmp, "consumer")
	if err := os.Mkdir(consumer, 0700); err != nil {
		return err
	}
	probe, err := os.ReadFile(filepath.Join(dest, "testdata", "consumer_test.go"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(consumer, "consumer_test.go"), probe, 0600); err != nil {
		return err
	}
	var caller modfile.File
	if err := caller.AddModuleStmt("example.invalid/sdk-consumer"); err != nil {
		return err
	}
	goVersion := "1.16"
	if manifest.Go != nil {
		goVersion = manifest.Go.Version
	}
	if err := caller.AddGoStmt(goVersion); err != nil {
		return err
	}
	if err := caller.AddRequire(module, "v0.0.0"); err != nil {
		return err
	}
	if err := caller.AddReplace(module, "", "../sdk", ""); err != nil {
		return err
	}
	callerMod, err := caller.Format()
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(consumer, "go.mod"), callerMod, 0600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(consumer, "go.sum"), sum, 0600); err != nil {
		return err
	}
	args := []string{"test", "-mod=readonly", "-count=1"}
	if cross {
		args = append(args, "-c", "-o", os.DevNull)
	}
	if _, err = runGo(dest, append(args, "./...")...); err != nil {
		return err
	}
	// Resolve only the consumer build, including its tests. Tidy would also
	// resolve the tests of third-party suppliers outside the consumer build.
	if _, err := runGo(consumer, "list", "-mod=mod", "-deps", "-test", "."); err != nil {
		return err
	}
	if cross {
		_, err := runGo(consumer, append(args, ".")...)
		return err
	}
	evidence := filepath.Join(tmp, "operations")
	if err := os.Mkdir(evidence, 0700); err != nil {
		return err
	}
	if err := instrumentConsumer(consumer, module, evidence); err != nil {
		return err
	}
	profile := filepath.Join(tmp, "consumer.cover")
	output, err := runGo(consumer, append(args, "-json", "-coverpkg="+module+"/...", "-coverprofile="+profile, ".")...)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(output))
	for {
		var event struct{ Action, Test string }
		if err := dec.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("external consumer ran no passing tests")
			}
			return err
		}
		if event.Action == "pass" && event.Test != "" {
			return checkConsumerCoverage(dest, module, profile, evidence)
		}
	}
}

func runGo(dir string, args ...string) (string, error) {
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "GOWORK=off")
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("go %s: %w\n%s%s", args[0], err, out, exit.Stderr)
		}
		return "", err
	}
	return string(out), nil
}
