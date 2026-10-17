---
name: secure-defaults
description: Use when writing or reviewing Go code that generates tokens or keys, opens files named by users, configures crypto/tls, encrypts data or derives keys, renders HTML, compares secrets, sets cookies, or must run in FIPS 140-3 mode. Covers crypto/rand.Text, os.Root, leaving tls.Config defaults (post-quantum key exchange) alone, InsecureSkipVerify, stdlib AEAD/HKDF/SHA-3/ML-KEM, html/template, constant-time comparison, cookie flags and GOFIPS140.
---

# Secure defaults

## The decision

Since Go 1.24 the standard library ships the secure answer for most everyday security work: a CSPRNG that cannot fail, file access that cannot escape a directory, TLS that negotiates post-quantum key exchange without configuration, an AEAD that manages its own nonces, and a FIPS 140-3 module selected at build time. The decision is whether to trust those defaults or to override them with code copied from older answers: `math/rand` tokens, `filepath.Join` with a prefix check, a pinned cipher list, `InsecureSkipVerify` to get past a private CA, hand-rolled nonces. Every override looks harmless in review and silently removes a protection the default gave you for free. This skill takes one position: use the standard library's secure default, never switch it off, never roll your own. HTTP server timeouts and body limits belong to the `http-server` skill.

## The rule

1. Generate tokens, IDs and secrets with `crypto/rand` (`rand.Text()` for strings, `rand.Read` for bytes), never with `math/rand` or `math/rand/v2`.
2. Open any path that comes from a user, an archive or a request through `os.Root` (`os.OpenRoot`, or `os.OpenInRoot` for one file), never through `filepath.Join` plus a prefix check.
3. Leave `CurvePreferences`, `CipherSuites` and `MinVersion` unset in `tls.Config`, and set only what you must provide: certificates, `RootCAs`, `ClientCAs`, `ServerName`.
4. Set `InsecureSkipVerify` only in tests; trust a private CA by adding it to `RootCAs`, and put any extra check in `VerifyConnection`.
5. Encrypt and derive keys with the standard library: `cipher.NewGCMWithRandomNonce`, `crypto/hkdf`, `crypto/sha3`, `crypto/mlkem`, RSA-OAEP, and never CFB, OFB, caller-managed nonces or PKCS #1 v1.5 encryption.
6. Render HTML with `html/template` and never convert untrusted input to `template.HTML`, `template.JS`, `template.URL` or any other trusted type.
7. Compare secrets with `crypto/subtle.ConstantTimeCompare` (or `hmac.Equal`) over fixed-length values, and set `Secure`, `HttpOnly` and `SameSite` on every session cookie.
8. Get FIPS 140-3 by building with `GOFIPS140` and checking `crypto/fips140.Enabled()` at startup, never with Go+BoringCrypto or `GODEBUG=fips140=only` in production.

## Why

**Rule 1.** `math/rand` and `math/rand/v2` are fast, not unpredictable: an attacker who sees enough outputs can predict the next session token or reset link. `rand.Text()` (Go 1.24) returns base32 text with at least 128 bits of randomness, which is the whole token function. Since Go 1.24 `crypto/rand.Read` never returns an error and always fills the buffer, so an `if err != nil` branch after it is dead code. Since Go 1.26 the random parameter of `rsa.GenerateKey`, `ecdsa.GenerateKey` and `Sign`, `ecdh.Curve.GenerateKey`, `rand.Prime` and others is ignored and a secure source is always used: passing a seeded reader to get deterministic keys does nothing. For deterministic tests use `testing/cryptotest.SetGlobalRandom`. The `cryptocustomrand=1` GODEBUG that restores the old behavior is temporary; do not build on it.

**Rule 2.** `filepath.Join(base, name)` cleans `..`, so the prefix check passes, but it cannot see symlinks: anyone who can create a link inside the upload directory (an extracted archive, another tenant, a compromised process) can point it at `/etc` and the check still passes. Checking first and opening later is also a race. Methods on `os.Root` resolve every component inside the directory and refuse names that escape it, including through symlinks. `os.OpenRoot` and `os.OpenInRoot` arrived in Go 1.24; `ReadFile`, `WriteFile`, `MkdirAll`, `Rename`, `RemoveAll` and the rest of the file API on `Root` arrived in Go 1.25, so on the 1.26 floor there is no reason to drop back to plain `os` functions for untrusted names. Use `Root.FS()` when an `fs.FS` is needed; `os.DirFS` does not stop symlink escapes.

**Rule 3.** With `CurvePreferences` empty, the client and server offer `X25519MLKEM768` (Go 1.24) and `SecP256r1MLKEM768` and `SecP384r1MLKEM1024` (Go 1.26), hybrid post-quantum key exchanges that protect recorded traffic against a future quantum computer. Setting `CurvePreferences` to anything replaces that list, so `[]tls.CurveID{tls.X25519, tls.CurveP256}` turns post-quantum off with no warning; the order is ignored since Go 1.24 anyway. `CipherSuites` only affects TLS 1.0 to 1.2 (TLS 1.3 suites are not configurable), and a nil list is the safe default that each release tightens (RSA key exchange and 3DES are already out); a pinned list freezes today's choices forever. `MinVersion` already defaults to TLS 1.2 for clients and servers, so `MinVersion: tls.VersionTLS12` is noise and any lower value is a downgrade.

**Rule 4.** `InsecureSkipVerify: true` accepts any certificate for any host, so anyone on the path can impersonate the server. It usually appears to get past a private or self-signed CA; the fix is a `x509.CertPool` with that CA in `RootCAs`. The `crypto/tls` docs say the flag is only for testing or together with your own verification. If you need checks beyond the chain (pinning a SPIFFE ID, a specific SAN), add them in `VerifyConnection` on top of normal verification. `VerifyConnection` runs for every connection, including resumed ones; `VerifyPeerCertificate` is not called on resumption, so a check placed only there can be skipped. In tests, `httptest.NewTLSServer` plus `srv.Client()` or `srv.Certificate()` avoids the flag entirely.

**Rule 5.** CFB and OFB are deprecated since Go 1.24 because they are unauthenticated: an attacker flips ciphertext bits and the matching plaintext bits flip, with no error. Plain GCM is authenticated but leaves the nonce to the caller, and one reused nonce breaks both confidentiality and integrity. `cipher.NewGCMWithRandomNonce` (Go 1.24) generates a random 96-bit nonce on every `Seal` and prepends it; the cost is 28 bytes of overhead and a limit of 2^32 messages per key, so derive per-purpose keys with `crypto/hkdf`. `crypto/hkdf`, `crypto/pbkdf2`, `crypto/sha3` and `crypto/mlkem` moved into the standard library in Go 1.24, where they are part of the audited FIPS module; replace the `golang.org/x/crypto` copies. RSA PKCS #1 v1.5 encryption is deprecated since Go 1.26; use `rsa.EncryptOAEP`. Password hashing is the one gap: the standard library has no argon2id or bcrypt, so use `golang.org/x/crypto/argon2` or `bcrypt`, and use `crypto/pbkdf2` only when FIPS requires it.

**Rule 6.** `html/template` escapes each value for the context it lands in: element text, attribute, URL, JavaScript, CSS. Converting input to `template.HTML` (or `JS`, `URL`, `HTMLAttr`, `CSS`) tells it the value is already safe, so a `<script>` in a user's display name runs in every visitor's browser. `text/template` never escapes and must not produce HTML. If you need rich text from users, sanitize it with an allow-list sanitizer first, then convert the sanitized output only.

**Rule 7.** `==` on strings returns at the first differing byte, so response timing tells an attacker how many leading bytes of a guessed token are correct. `subtle.ConstantTimeCompare` takes time independent of the content but returns immediately when the lengths differ, so compare fixed-length values: a SHA-256 of both sides, or two HMACs with `hmac.Equal`. For cookies, `Secure` keeps the token off plain HTTP, `HttpOnly` keeps it away from injected scripts, and `SameSite=Lax` stops browsers from sending it on cross-site subrequests and form posts.

**Rule 8.** `GOFIPS140` selects the Go Cryptographic Module at build time: `certified` (the latest version with a CMVP certificate), `inprocess`, a pinned version such as `v1.0.0` (CMVP certificate #5247) or `v1.26.0`, or `latest`. Any of these turns FIPS mode on by default, and applications need no code changes; the setting cannot be changed after start. `GODEBUG=fips140=only` makes non-approved algorithms fail or panic; the Go docs call it a best-effort testing mode "not intended to be used in production". Go+BoringCrypto is meant to be removed and is incompatible with the native module. Assert `fips140.Enabled()` at startup so a build without `GOFIPS140` fails loudly instead of running uncertified.

The cost: trusting defaults means your security posture changes when you upgrade Go, because each release tightens them. That is the point. Read the `crypto/tls` and `crypto` sections of the release notes on every upgrade, and pin a GODEBUG only to buy time while you fix an incompatible peer.

## Good example

See [`examples/good/`](examples/good/).

```go
func newSessionToken() string {
	return rand.Text()
}

func readUpload(root *os.Root, name string) ([]byte, error) {
	return root.ReadFile(name)
}

func serverTLS(cert tls.Certificate) *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{cert}}
}

func tokenMatches(got, want string) bool {
	g := sha256.Sum256([]byte(got))
	w := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(g[:], w[:]) == 1
}
```

Each function delegates to the standard library and configures nothing it does not have to. The tests show `os.Root` rejecting `../`, nested `..`, absolute paths and an escaping symlink, sealed ciphertexts that differ for the same plaintext, and a default client and server negotiating `X25519MLKEM768` over loopback, with the private CA trusted through `RootCAs`.

## Bad example

See [`examples/bad/`](examples/bad/).

```go
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

func render(name string) error {
	// MISTAKE: template.HTML tells html/template the input is already safe, so <script> runs in the page.
	return greeting.Execute(os.Stdout, template.HTML(name))
}
```

The rest of the file has the other common overrides: a `math/rand` token, `filepath.Join` with a `strings.HasPrefix` check, AES-CFB with a caller-supplied IV, `==` on tokens and a cookie with no flags. Each one is what older tutorials and code generators still produce. It compiles and `go vet` reports none of it; gosec flags several (G402, G404, G203, G304).

## Checklist

When reviewing code under this skill, confirm:

- [ ] No token, ID, nonce, key or password reset code comes from `math/rand` or `math/rand/v2`, and there is no dead `err` check after `crypto/rand.Read`.
- [ ] Every file path built from user input, archive entries or request data is opened through an `os.Root`, not `filepath.Join` with a prefix check.
- [ ] No `tls.Config` sets `CurvePreferences`, `CipherSuites`, or a `MinVersion` below TLS 1.2.
- [ ] `InsecureSkipVerify` appears only in `_test.go` files, and custom certificate checks live in `VerifyConnection`.
- [ ] Encryption uses `cipher.NewGCMWithRandomNonce` or another AEAD with no nonce chosen by the caller, and nothing uses CFB, OFB or PKCS #1 v1.5 encryption.
- [ ] HKDF, PBKDF2, SHA-3 and ML-KEM are imported from the standard library, not `golang.org/x/crypto`.
- [ ] HTML is produced only by `html/template`, and no untrusted value is converted to `template.HTML`, `JS`, `URL`, `HTMLAttr` or `CSS`.
- [ ] Secrets are compared with `subtle.ConstantTimeCompare` or `hmac.Equal` over fixed-length values, never with `==`.
- [ ] Session cookies set `Secure`, `HttpOnly` and `SameSite`.
- [ ] A FIPS build uses `GOFIPS140`, checks `fips140.Enabled()` at startup, and does not use BoringCrypto or `fips140=only`.

## When not to apply

- **Non-security randomness.** Jitter for retries, load-balancer picks, shuffling test data and simulations should use `math/rand/v2`: it is faster and predictability does no harm there.
- **Paths you control.** A path from your own config or a constant does not need `os.Root`. When attackers cannot touch the filesystem at all and you only need a lexical check (a key in an object store, a route segment), `filepath.IsLocal` is enough. Note that `os.Root` on Unix does not stop traversal across bind mounts, so it is not a sandbox for hostile local users.
- **A peer that breaks on the defaults.** Some old middleboxes and servers fail on the larger post-quantum ClientHello. Set `GODEBUG=tlsmlkem=0` (or `tlssecpmlkem=0`) for that binary, with a ticket to remove it, instead of pinning `CurvePreferences` in code. When every client is modern, raising `MinVersion` to `tls.VersionTLS13` is a valid tightening, not a weakening.
- **Compliance or interop that dictates parameters.** A regulator or a partner contract may require an exact suite or curve list. Set it, cite the requirement in a comment next to the field, and include the post-quantum hybrids if the requirement allows them.
- **Tests.** `InsecureSkipVerify`, `testing/cryptotest.SetGlobalRandom` and `//go:debug` settings such as `rsa1024min=0` belong in test files only; prefer `httptest` clients, which trust the test certificate without skipping verification.
- **Interop with existing ciphertext.** Data already encrypted with CBC, CFB or PKCS #1 v1.5 must still be decrypted. Decrypt with the old mode, re-encrypt with an AEAD, and never write new data the old way.
- **Trusted HTML from your own code.** A constant snippet or the output of a vetted sanitizer may be converted to `template.HTML`; mark the conversion with a comment saying where the HTML comes from.
