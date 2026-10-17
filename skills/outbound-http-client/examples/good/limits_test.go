package upstream

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newClient(t *testing.T, h http.HandlerFunc, timeout time.Duration) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, timeout)
	if err != nil {
		t.Fatal(err)
	}
	return c, srv
}

func TestBodyLimitIsEnforced(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("x", maxBody+1)))
	}, time.Second)

	if _, err := c.Get(t.Context(), "/huge"); !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
}

func TestTimeoutIsHonored(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}, 50*time.Millisecond)

	start := time.Now()
	_, err := c.Get(t.Context(), "/slow")
	ue, ok := errors.AsType[*url.Error](err)
	if !ok || !ue.Timeout() {
		t.Fatalf("err = %v, want a timeout *url.Error", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("call took %v, want about 50ms", d)
	}
}

func TestRedirectToOtherHostIsRefused(t *testing.T) {
	var hit atomic.Bool
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Store(true)
	}))
	t.Cleanup(evil.Close)
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+"/steal", http.StatusFound)
	}, time.Second)

	if _, err := c.Get(t.Context(), "/moved"); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("err = %v, want ErrNotAllowed", err)
	}
	if hit.Load() {
		t.Error("client followed the redirect to a host outside the allow-list")
	}
}

func TestAbsoluteURLOutsideAllowListIsRefused(t *testing.T) {
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {}, time.Second)

	for _, ref := range []string{"http://169.254.169.254/latest/meta-data/", "//localhost:6060/debug/pprof/"} {
		if _, err := c.Get(t.Context(), ref); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("Get(%q) err = %v, want ErrNotAllowed", ref, err)
		}
	}
}
