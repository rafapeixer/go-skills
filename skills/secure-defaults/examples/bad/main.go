// Command bad compiles, but every marked line switches off a secure default
// or replaces it with a hand-rolled version that is easier to break.
package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/tls"
	"fmt"
	"html/template"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const uploadDir = "/srv/uploads"

func newSessionToken() string {
	// MISTAKE: math/rand is not a CSPRNG; an attacker who sees a few outputs can predict the next tokens.
	return fmt.Sprintf("%016x", rand.Uint64())
}

func readUpload(name string) ([]byte, error) {
	// MISTAKE: Join plus a prefix check is lexical; a symlink inside uploadDir still reads files outside it.
	path := filepath.Join(uploadDir, name)
	if !strings.HasPrefix(path, uploadDir) {
		return nil, fmt.Errorf("bad path %q", name)
	}
	return os.ReadFile(path)
}

var serverTLS = &tls.Config{
	MinVersion: tls.VersionTLS12,
	// MISTAKE: pinning curves drops the default X25519MLKEM768 hybrid, so traffic recorded today can be decrypted by a future quantum computer.
	CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256},
	// MISTAKE: a frozen suite list copied from a blog post never picks up later fixes to Go's defaults.
	CipherSuites: []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA, tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256},
}

// MISTAKE: skipping verification to "fix" a private CA lets anyone on the path impersonate the server.
var internalClient = &http.Client{Transport: &http.Transport{
	TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
}}

func encrypt(key, iv, plaintext []byte) []byte {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	out := make([]byte, len(plaintext))
	// MISTAKE: CFB is deprecated and unauthenticated; flipped ciphertext bits flip plaintext bits undetected,
	// and the caller has to invent and never reuse an IV.
	cipher.NewCFBEncrypter(block, iv).XORKeyStream(out, plaintext)
	return out
}

func tokenMatches(got, want string) bool {
	// MISTAKE: == returns at the first differing byte, so response timing reveals how much of the token is right.
	return got == want
}

func sessionCookie(token string) *http.Cookie {
	// MISTAKE: no Secure, HttpOnly or SameSite; the cookie travels over HTTP, is readable by injected
	// scripts and is sent on cross-site requests.
	return &http.Cookie{Name: "session", Value: token, Path: "/"}
}

var greeting = template.Must(template.New("g").Parse(`<p>Hello, {{.}}</p>`))

func render(name string) error {
	// MISTAKE: template.HTML tells html/template the input is already safe, so <script> runs in the page.
	return greeting.Execute(os.Stdout, template.HTML(name))
}

func main() {
	token := newSessionToken()
	log.Println(tokenMatches(token, token), sessionCookie(token).String())
	_, _ = readUpload("report.txt")
	_ = encrypt(make([]byte, 16), make([]byte, aes.BlockSize), []byte("hi"))
	_ = render(`<script>alert(1)</script>`)
}
