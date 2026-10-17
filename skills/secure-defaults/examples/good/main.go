// Command good keeps every security default the standard library gives:
// random tokens from crypto/rand, user paths through os.Root, an untouched
// tls.Config, constant-time token checks, hardened cookies and html/template.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"html/template"
	"log"
	"net/http"
	"os"
	"time"
)

// newSessionToken returns at least 128 bits of randomness as base32 text.
func newSessionToken() string {
	return rand.Text()
}

// readUpload opens a user-named file. The root rejects "..", absolute paths
// and symlinks that point outside the directory.
func readUpload(root *os.Root, name string) ([]byte, error) {
	return root.ReadFile(name)
}

// serverTLS sets only what the server must provide. Versions, cipher suites
// and key exchanges (post-quantum hybrids included) stay on Go's defaults.
func serverTLS(cert tls.Certificate) *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{cert}}
}

// clientTLS trusts a private CA by adding it to RootCAs, so verification
// stays on instead of being skipped.
func clientTLS(ca *x509.Certificate) *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return &tls.Config{RootCAs: pool}
}

// tokenMatches compares fixed-length digests in constant time, so neither
// the content nor the length of the expected token leaks through timing.
func tokenMatches(got, want string) bool {
	g := sha256.Sum256([]byte(got))
	w := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(g[:], w[:]) == 1
}

func sessionCookie(token string) *http.Cookie {
	return &http.Cookie{
		Name:     "session",
		Value:    token,
		Path:     "/",
		MaxAge:   int((12 * time.Hour).Seconds()),
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

// greeting escapes .Name for the HTML context it lands in.
var greeting = template.Must(template.New("greeting").Parse(`<p>Hello, {{.Name}}</p>`))

func main() {
	dir, err := os.MkdirTemp("", "uploads")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	root, err := os.OpenRoot(dir)
	if err != nil {
		log.Fatal(err)
	}
	defer root.Close()

	if _, err := readUpload(root, "../../etc/passwd"); err != nil {
		log.Printf("traversal rejected: %v", err)
	}

	token := newSessionToken()
	log.Printf("token matches itself: %v", tokenMatches(token, token))
	log.Printf("cookie: %s", sessionCookie(token).String())

	if err := greeting.Execute(os.Stdout, struct{ Name string }{`<script>alert(1)</script>`}); err != nil {
		log.Fatal(err)
	}
}
