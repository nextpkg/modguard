package sdk

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestGeneratedSDKsPassCheck(t *testing.T) {
	root := fixture(t)
	dir := filepath.Join(root, "sdks")
	for _, trial := range []struct{ kind, name, op, supplier string }{
		{"single", "wechatlogin", "Login", ""},
		{"multi", "stt", "Transcribe", "volcengine"},
		{"multi", "cfg", "Load", "cfgpkg"},
		{"multi", "context", "Capture", "context"},
	} {
		if err := generate(dir, trial.kind, trial.name, trial.op, trial.supplier); err != nil {
			t.Fatal(err)
		}
	}
	count, problems, err := check(dir)
	if err != nil || len(problems) != 0 || count != 6 {
		t.Fatalf("count=%d problems=%v err=%v", count, problems, err)
	}
}

func TestGenerationProtectsDestination(t *testing.T) {
	dir := filepath.Join(fixture(t), "sdks")
	for _, trial := range []struct{ kind, name, op, supplier string }{
		{"single", "../escape", "Login", ""},
		{"multi", "speech", "Run", "../escape"},
		{"multi", "speech", "Run", ""},
		{"single", "speech", "", ""},
		{"single", "speech", "login", ""},
		{"single", "speech", "Run", "one"},
	} {
		if err := generate(dir, trial.kind, trial.name, trial.op, trial.supplier); err == nil {
			t.Fatalf("accepted %+v", trial)
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { results <- generate(dir, "single", "locker", "Lock", "") })
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("concurrent generation succeeded %d times", success)
	}
	writeFixture(t, dir, "locker/config.go", "// user edit\n")
	if err := generate(dir, "single", "locker", "Unlock", ""); err == nil {
		t.Fatal("replaced existing SDK")
	}
	got, err := os.ReadFile(filepath.Join(dir, "locker", "config.go"))
	if err != nil || string(got) != "// user edit\n" {
		t.Fatalf("lost existing source: %q %v", got, err)
	}
}
