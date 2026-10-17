// Command bad shows the plugin hosts that quick-starts and tutorials still produce.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"plugin"
	"strconv"
	"strings"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// MISTAKE: plugin.Open only works on Linux, FreeBSD and macOS, the .so must be
// built with exactly the same toolchain, tags and dependency versions as the
// host, it can never be unloaded, and its code runs with full process access.
func loadNative(path string) (func(string) string, error) {
	p, err := plugin.Open(path)
	if err != nil {
		return nil, err
	}
	sym, err := p.Lookup("Transform")
	if err != nil {
		return nil, err
	}
	return sym.(func(string) string), nil
}

type host struct {
	rt   wazero.Runtime
	wasm []byte
}

func newHost(ctx context.Context, wasm []byte) *host {
	// MISTAKE: no WithCloseOnContextDone, so a guest stuck in a loop holds its
	// goroutine forever, and no WithMemoryLimitPages, so it may grow to 4 GiB.
	rt := wazero.NewRuntime(ctx)
	wasi_snapshot_preview1.MustInstantiate(ctx, rt)
	return &host{rt: rt, wasm: wasm}
}

func (h *host) handle(w http.ResponseWriter, r *http.Request) {
	a, _ := strconv.ParseUint(r.FormValue("a"), 10, 32)
	b, _ := strconv.ParseUint(r.FormValue("b"), 10, 32)
	// MISTAKE: Background drops the request's cancellation and sets no deadline.
	ctx := context.Background()

	cfg := wazero.NewModuleConfig().
		WithName("").
		WithStartFunctions("_initialize").
		// MISTAKE: mounts the host root read-write: the plugin can read keys
		// and configs and overwrite any file the service user can write.
		WithFSConfig(wazero.NewFSConfig().WithDirMount("/", "/")).
		// MISTAKE: copies a host secret into the guest's environment; wazero
		// grants no environment variables unless you add them.
		WithEnv("DATABASE_URL", os.Getenv("DATABASE_URL"))

	// MISTAKE: Runtime.Instantiate compiles the module on every request, which
	// costs far more than the call itself; CompileModule once at load.
	mod, err := h.rt.InstantiateWithConfig(ctx, h.wasm, cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// MISTAKE: the instance is never closed, so its memory stays with the runtime.
	res, err := mod.ExportedFunction("add").Call(ctx, a, b)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintln(w, int32(res[0]))
}

func main() {
	if len(os.Args) == 3 && strings.HasSuffix(os.Args[1], ".so") {
		transform, err := loadNative(os.Args[1])
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(transform(os.Args[2]))
		return
	}
	wasm, err := os.ReadFile(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	h := newHost(context.Background(), wasm)
	http.HandleFunc("/add", h.handle)
	log.Fatal(http.ListenAndServe(":8080", nil))
}
