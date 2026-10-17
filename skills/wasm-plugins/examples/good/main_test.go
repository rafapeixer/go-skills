package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/tetratelabs/wazero/sys"
)

// newHost builds testdata/guest as a WASI reactor with the toolchain running
// the test, so no .wasm binary is committed, and loads it into a Host.
func newHost(t *testing.T) *Host {
	t.Helper()
	out := filepath.Join(t.TempDir(), "plugin.wasm")
	goBin := "go"
	if root := runtime.GOROOT(); root != "" {
		goBin = filepath.Join(root, "bin", "go")
	}
	cmd := exec.Command(goBin, "build", "-buildmode=c-shared", "-o", out, "./testdata/guest")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build guest: %v\n%s", err, b)
	}
	wasm, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHost(t.Context(), wasm)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close(context.Background()) })
	return h
}

func TestPlugin(t *testing.T) {
	h := newHost(t)

	if res, err := h.Call(t.Context(), nil, "add", 2, 3); err != nil || int32(res[0]) != 5 {
		t.Errorf("add(2, 3) = %v, %v; want 5", res, err)
	}

	_, err := h.Call(t.Context(), nil, "spin")
	exit, ok := errors.AsType[*sys.ExitError](err)
	if !ok || exit.ExitCode() != sys.ExitCodeDeadlineExceeded || !errors.Is(err, errPluginBudget) {
		t.Errorf("spin: err = %v; want a deadline exit caused by %v", err, errPluginBudget)
	}

	if _, err := h.Call(t.Context(), nil, "grow", 128); err == nil {
		t.Error("grow(128 MiB) succeeded under a 64 MiB limit")
	}

	// Compiled once, instantiated per call: concurrent calls share no state.
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			if res, err := h.Call(t.Context(), nil, "add", uint64(i), 1); err != nil || int32(res[0]) != int32(i)+1 {
				t.Errorf("add(%d, 1) = %v, %v", i, res, err)
			}
		})
	}
	wg.Wait()

	readData := func(data fs.FS) int32 {
		t.Helper()
		res, err := h.Call(t.Context(), data, "readData")
		if err != nil {
			t.Fatal(err)
		}
		return int32(res[0])
	}
	dir := t.TempDir()
	tenant := filepath.Join(dir, "tenant")
	if err := errors.Join(
		os.Mkdir(tenant, 0o755),
		os.WriteFile(filepath.Join(tenant, "input.txt"), []byte("hello"), 0o644),
		os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("s3cr3t"), 0o644),
	); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(tenant)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })

	if n := readData(nil); n != -1 {
		t.Errorf("readData with no mount = %d; want -1", n)
	}
	if n := readData(root.FS()); n != 5 {
		t.Errorf("readData with tenant mount = %d; want 5", n)
	}
	// A symlink planted in the tenant directory must not reach the host.
	link := filepath.Join(tenant, "input.txt")
	if err := errors.Join(os.Remove(link), os.Symlink(filepath.Join("..", "secret.txt"), link)); err != nil {
		t.Skipf("cannot plant a symlink here: %v", err)
	}
	if n := readData(root.FS()); n != -1 {
		t.Errorf("readData through escaping symlink = %d; want -1", n)
	}
}
