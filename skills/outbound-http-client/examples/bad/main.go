// Command bad compiles and works against a healthy upstream on a quiet day,
// but every marked line is a mistake that exhausts ports and memory under
// load, hangs on a slow upstream, or lets a caller reach internal hosts.
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// MISTAKE: follows any number of redirects to any host, and copies the token
// that the Client deliberately drops on a cross-domain redirect, so one
// redirect to another host hands it the bearer token (gosec G119).
var apiClient = &http.Client{
	Timeout: 10 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		req.Header.Set("Authorization", via[0].Header.Get("Authorization"))
		return nil
	},
}

func fetchUser(ctx context.Context, id string) ([]byte, error) {
	// MISTAKE: a new Client and Transport per call; every call pays a TCP and
	// TLS handshake, and a zero Transport has no IdleConnTimeout, so each
	// abandoned pool holds its idle connection until the server hangs up.
	c := &http.Client{Transport: &http.Transport{}, Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://users.internal/users/"+id, nil)
	if err != nil {
		return nil, err
	}
	// MISTAKE: setting Accept-Encoding by hand turns off transparent gzip;
	// the body below is returned still gzip-compressed.
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		// MISTAKE: returns without closing the body; the connection stays
		// checked out until the Timeout fires, and is never reused.
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	// MISTAKE: unbounded read; one huge or endless response exhausts memory.
	return io.ReadAll(resp.Body)
}

func fetchAvatar(rawURL string) (int, error) {
	// MISTAKE: http.Get uses DefaultClient, which has no timeout and no ctx, so
	// a stalled upstream blocks this goroutine forever; and rawURL comes from
	// the user, so http://169.254.169.254/ reads cloud credentials (SSRF, G107).
	resp, err := http.Get(rawURL)
	if err != nil {
		return 0, err
	}
	// MISTAKE: closes without reading to EOF, so the connection is not reused.
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

func fetchProfile(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.example.com/me", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := apiClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(io.Discard, resp.Body)
	return err
}

func main() {
	if len(os.Args) < 3 {
		return
	}
	user, err := fetchUser(context.Background(), os.Args[1])
	fmt.Println(len(user), err)
	fmt.Println(fetchAvatar(os.Args[2]))
	fmt.Println(fetchProfile(context.Background(), os.Getenv("API_TOKEN")))
}
