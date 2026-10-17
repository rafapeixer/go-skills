// Package upstream is the client for one upstream service: one *http.Client
// with a cloned Transport, a total timeout, bounded body reads, and an
// allow-list that covers both the URLs it is given and the redirects it follows.
package upstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	maxBody  = 1 << 20  // largest response this upstream legitimately sends
	maxDrain = 64 << 10 // leftover bytes worth reading to keep the connection
)

var (
	ErrNotAllowed = errors.New("upstream: url not allowed")
	ErrTooLarge   = errors.New("upstream: response body too large")
)

// Client is created once per upstream in main and shared by every request.
type Client struct {
	base *url.URL
	hc   *http.Client
}

func New(baseURL string, timeout time.Duration) (*Client, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}
	c := &Client{base: base}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = 64 // default is 2; size it to concurrent calls to this host
	c.hc = &http.Client{
		Transport: t,
		Timeout:   timeout, // whole exchange: dial, TLS, redirects, body read
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("upstream: too many redirects")
			}
			return c.allow(req.URL)
		},
	}
	return c, nil
}

// allow accepts only the configured scheme and host (host includes the port).
func (c *Client) allow(u *url.URL) error {
	if u.Scheme != c.base.Scheme || u.Host != c.base.Host {
		return fmt.Errorf("%w: %s://%s", ErrNotAllowed, u.Scheme, u.Host)
	}
	return nil
}

// Get fetches ref, a path or an absolute URL (for example a "next" link the
// caller received), resolved against the base URL and checked before dialing.
func (c *Client) Get(ctx context.Context, ref string) ([]byte, error) {
	r, err := url.Parse(ref)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", ref, err)
	}
	u := c.base.ResolveReference(r)
	if err := c.allow(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	// No Accept-Encoding: the Transport asks for gzip and decodes it for us.
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", u.Path, err)
	}
	defer func() {
		// Drain on every path, error paths included, so the connection is reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrain))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get %s: status %d", u.Path, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", u.Path, err)
	}
	if len(body) > maxBody {
		return nil, ErrTooLarge
	}
	return body, nil
}
