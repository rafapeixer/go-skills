// Command good runs a third-party plugin as a WebAssembly module in wazero:
// compiled once, instantiated per call, with explicit capabilities and bounds.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

const (
	maxPages    = 1024 // 64 MiB: a Wasm page is 64 KiB
	callTimeout = 200 * time.Millisecond
)

var errPluginBudget = errors.New("plugin call budget exceeded")

// Host owns one runtime and one compiled plugin; both are safe to share.
type Host struct {
	rt     wazero.Runtime
	plugin wazero.CompiledModule
}

func NewHost(ctx context.Context, wasm []byte) (*Host, error) {
	cfg := wazero.NewRuntimeConfig().
		WithCloseOnContextDone(true).
		WithMemoryLimitPages(maxPages)
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		return nil, errors.Join(fmt.Errorf("instantiate wasi: %w", err), rt.Close(ctx))
	}
	plugin, err := rt.CompileModule(ctx, wasm)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("compile plugin: %w", err), rt.Close(ctx))
	}
	return &Host{rt: rt, plugin: plugin}, nil
}

// Call runs one export in a fresh instance. data, from os.Root.FS so symlinks
// cannot escape, is all the guest sees, read-only at /data; nil grants nothing.
func (h *Host) Call(ctx context.Context, data fs.FS, fn string, args ...uint64) ([]uint64, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, callTimeout, errPluginBudget)
	defer cancel()

	cfg := wazero.NewModuleConfig().WithName("").WithStartFunctions("_initialize")
	if data != nil {
		cfg = cfg.WithFSConfig(wazero.NewFSConfig().WithFSMount(data, "/data"))
	}
	mod, err := h.rt.InstantiateModule(ctx, h.plugin, cfg)
	if err != nil {
		return nil, fmt.Errorf("instantiate plugin: %w", err)
	}
	defer mod.Close(context.WithoutCancel(ctx))

	f := mod.ExportedFunction(fn)
	if f == nil {
		return nil, fmt.Errorf("plugin has no export %q", fn)
	}
	res, err := f.Call(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("call %s: %w", fn, errors.Join(err, context.Cause(ctx)))
	}
	return res, nil
}

func (h *Host) Close(ctx context.Context) error { return h.rt.Close(ctx) }

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: good plugin.wasm")
	}
	wasm, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	h, err := NewHost(ctx, wasm)
	if err != nil {
		return err
	}
	defer h.Close(ctx)
	res, err := h.Call(ctx, nil, "add", 2, 3)
	if err != nil {
		return err
	}
	fmt.Println(int32(res[0]))
	return nil
}
