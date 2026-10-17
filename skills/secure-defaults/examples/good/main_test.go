package main

import (
	"bytes"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadUploadStaysInRoot(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "uploads")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "secret.txt"), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	if got, err := readUpload(root, "a.txt"); err != nil || string(got) != "ok" {
		t.Fatalf("readUpload(a.txt) = %q, %v", got, err)
	}
	for _, name := range []string{"../secret.txt", "sub/../../secret.txt", filepath.Join(parent, "secret.txt")} {
		if _, err := readUpload(root, name); err == nil {
			t.Errorf("readUpload(%q) succeeded; the root let the path escape", name)
		}
	}

	if err := os.Symlink(filepath.Join(parent, "secret.txt"), filepath.Join(dir, "link")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	if _, err := readUpload(root, "link"); err == nil {
		t.Error("readUpload(link) followed a symlink out of the root")
	}
}

func TestSessionTokensAreUniqueAndLong(t *testing.T) {
	a, b := newSessionToken(), newSessionToken()
	if a == b {
		t.Fatal("two tokens are equal")
	}
	if len(a) < 26 { // 26 base32 characters carry 130 bits
		t.Errorf("token %q is too short", a)
	}
}

func TestTokenMatches(t *testing.T) {
	tok := newSessionToken()
	if !tokenMatches(tok, tok) {
		t.Error("equal tokens did not match")
	}
	if tokenMatches(tok[:len(tok)-1], tok) || tokenMatches("", tok) {
		t.Error("different tokens matched")
	}
}

func TestSealRoundTrip(t *testing.T) {
	aead, err := newAEAD([]byte("master secret from a secret store"), nil, "session-cookie v1")
	if err != nil {
		t.Fatal(err)
	}
	c1, c2 := seal(aead, []byte("hi"), nil), seal(aead, []byte("hi"), nil)
	if bytes.Equal(c1, c2) {
		t.Fatal("same plaintext produced the same ciphertext; the nonce was reused")
	}
	if got, err := open(aead, c1, nil); err != nil || string(got) != "hi" {
		t.Fatalf("open = %q, %v", got, err)
	}
	c1[len(c1)-1] ^= 1
	if _, err := open(aead, c1, nil); err == nil {
		t.Error("tampered ciphertext was accepted")
	}
}

func TestGreetingEscapes(t *testing.T) {
	var buf bytes.Buffer
	if err := greeting.Execute(&buf, struct{ Name string }{"<script>x</script>"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "<script>") {
		t.Errorf("output contains raw markup: %s", buf.String())
	}
}

func TestDefaultTLSNegotiatesPostQuantum(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLS(srv.Certificate())}}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.TLS.CurveID; got != tls.X25519MLKEM768 {
		t.Errorf("negotiated %v, want X25519MLKEM768", got)
	}
}
