package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
)

func newAuthTestServer(t *testing.T, origins string) *server {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "auth.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := &server{db: db, password: "secret", authOrigins: origins}
	if _, err = s.ownerAuthentication(); err != nil {
		t.Fatal(err)
	}
	return s
}
func authRequest(s *server, method, path, origin, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	r.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	return w
}
func authCookie(t *testing.T, w *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("missing %s cookie: %s", name, w.Body.String())
	return nil
}
func passwordSession(t *testing.T, s *server, origin string) *http.Cookie {
	t.Helper()
	w := authRequest(s, "POST", "/api/auth/login", origin, `{"password":"secret"}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	return authCookie(t, w, "video_server_auth")
}
func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

type virtualPasskey struct {
	id    []byte
	pub   ed25519.PublicKey
	priv  ed25519.PrivateKey
	owner []byte
	rp    string
}

func enrollVirtual(t *testing.T, s *server, origin string, session *http.Cookie) *virtualPasskey {
	t.Helper()
	w := authRequest(s, "POST", "/api/auth/passkeys/register/begin", origin, `{"name":"Test device"}`, session)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var options struct {
		PublicKey struct {
			Challenge              string
			RP                     struct{ ID string }
			User                   struct{ ID string }
			AuthenticatorSelection struct{ ResidentKey, UserVerification string }
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &options); err != nil {
		t.Fatal(err)
	}
	if options.PublicKey.AuthenticatorSelection.ResidentKey != "required" || options.PublicKey.AuthenticatorSelection.UserVerification != "required" {
		t.Fatal("credential requirements not enforced")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := base64.RawURLEncoding.DecodeString(options.PublicKey.User.ID)
	key := &virtualPasskey{id: []byte(randomAuthToken()), pub: pub, priv: priv, owner: owner, rp: options.PublicKey.RP.ID}
	client, _ := json.Marshal(map[string]any{"type": "webauthn.create", "challenge": options.PublicKey.Challenge, "origin": origin})
	rpHash := sha256.Sum256([]byte(key.rp))
	data := append([]byte{}, rpHash[:]...)
	data = append(data, 0x45, 0, 0, 0, 0)
	data = append(data, make([]byte, 16)...)
	data = binary.BigEndian.AppendUint16(data, uint16(len(key.id)))
	data = append(data, key.id...)
	cose, _ := cbor.Marshal(map[int]any{1: 1, 3: -8, -1: 6, -2: []byte(pub)})
	data = append(data, cose...)
	attestation, _ := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": data})
	payload, _ := json.Marshal(map[string]any{"id": b64(key.id), "rawId": b64(key.id), "type": "public-key", "response": map[string]any{"clientDataJSON": b64(client), "attestationObject": b64(attestation)}})
	w = authRequest(s, "POST", "/api/auth/passkeys/register/finish", origin, string(payload), session, authCookie(t, w, "video_server_ceremony"))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	return key
}
func virtualAssertion(t *testing.T, s *server, key *virtualPasskey, origin, clientOrigin string, flags byte) (string, *http.Cookie) {
	t.Helper()
	w := authRequest(s, "POST", "/api/auth/passkeys/login/begin", origin, `{}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var options struct {
		PublicKey struct {
			Challenge        string
			UserVerification string
		}
	}
	json.Unmarshal(w.Body.Bytes(), &options)
	if options.PublicKey.UserVerification != "required" {
		t.Fatal("verification not required")
	}
	client, _ := json.Marshal(map[string]any{"type": "webauthn.get", "challenge": options.PublicKey.Challenge, "origin": clientOrigin})
	rpHash := sha256.Sum256([]byte(key.rp))
	data := append([]byte{}, rpHash[:]...)
	data = append(data, flags, 0, 0, 0, 1)
	hash := sha256.Sum256(client)
	signed := append(append([]byte{}, data...), hash[:]...)
	sig := ed25519.Sign(key.priv, signed)
	payload, _ := json.Marshal(map[string]any{"id": b64(key.id), "rawId": b64(key.id), "type": "public-key", "response": map[string]any{"clientDataJSON": b64(client), "authenticatorData": b64(data), "signature": b64(sig), "userHandle": b64(key.owner)}})
	return string(payload), authCookie(t, w, "video_server_ceremony")
}
func TestPasskeyEnrollmentLoginAndPolicy(t *testing.T) {
	const origin = "https://video.example.com"
	s := newAuthTestServer(t, origin+",http://localhost:5000")
	session := passwordSession(t, s, origin)
	if !session.Secure || !session.HttpOnly || session.MaxAge != 30*24*3600 {
		t.Fatal("invalid session attributes")
	}
	key := enrollVirtual(t, s, origin, session)
	if w := authRequest(s, "PATCH", "/api/auth/password", origin, `{"enabled":false}`, session); w.Code != 403 {
		t.Fatal("password-only session disabled passwords")
	}
	payload, challenge := virtualAssertion(t, s, key, origin, origin, 5)
	w := authRequest(s, "POST", "/api/auth/passkeys/login/finish", origin, payload, challenge)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	session = authCookie(t, w, "video_server_auth")
	if w = authRequest(s, "POST", "/api/auth/passkeys/login/finish", origin, payload, challenge); w.Code != 400 {
		t.Fatal("replayed assertion accepted")
	}
	if w = authRequest(s, "PATCH", "/api/auth/password", origin, `{"enabled":false}`, session); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = authRequest(s, "DELETE", "/api/auth/passkeys/"+b64(key.id), origin, "", session); w.Code != 409 {
		t.Fatal("last credential removed")
	}
	if w = authRequest(s, "POST", "/api/auth/login", origin, `{"password":"secret"}`); w.Code != 401 {
		t.Fatal("disabled password accepted")
	}
	// Reloading with no password must preserve protection and session validation.
	restarted := &server{db: s.db, authOrigins: s.authOrigins}
	if !restarted.authEnabled() {
		t.Fatal("restart disabled protection")
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(session)
	if !restarted.isAuthenticated(r) {
		t.Fatal("session lost across restart")
	}
	if w = authRequest(s, "POST", "/api/auth/logout", origin, `{}`, session); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if s.isAuthenticated(r) {
		t.Fatal("logout did not revoke session")
	}
}
func TestPasskeyVerificationRejections(t *testing.T) {
	const origin = "https://video.example.com"
	for _, tc := range []struct {
		name, clientOrigin string
		flags              byte
		rp                 string
	}{{"wrong origin", "https://evil.example", 5, ""}, {"missing UV", origin, 1, ""}, {"wrong RP", origin, 5, "other.example"}} {
		t.Run(tc.name, func(t *testing.T) {
			s := newAuthTestServer(t, origin)
			session := passwordSession(t, s, origin)
			key := enrollVirtual(t, s, origin, session)
			if tc.rp != "" {
				key.rp = tc.rp
			}
			payload, c := virtualAssertion(t, s, key, origin, tc.clientOrigin, tc.flags)
			w := authRequest(s, "POST", "/api/auth/passkeys/login/finish", origin, payload, c)
			if w.Code != 401 {
				t.Fatalf("got %d %s", w.Code, w.Body.String())
			}
		})
	}
}
func TestLocalhostAndChallengeBinding(t *testing.T) {
	s := newAuthTestServer(t, "https://video.example.com,http://localhost:5000")
	origin := "http://localhost:5000"
	session := passwordSession(t, s, origin)
	if session.Secure {
		t.Fatal("localhost HTTP cookie is secure")
	}
	key := enrollVirtual(t, s, origin, session)
	payload, c := virtualAssertion(t, s, key, origin, origin, 5)
	if w := authRequest(s, "POST", "/api/auth/passkeys/login/finish", origin, payload); w.Code != 400 {
		t.Fatal("browser binding absent")
	}
	a, _ := s.ownerAuthentication()
	a.mu.Lock()
	ch := a.pending[a.sign(c.Value)]
	ch.expires = time.Now().Add(-time.Second)
	a.pending[a.sign(c.Value)] = ch
	a.mu.Unlock()
	if w := authRequest(s, "POST", "/api/auth/passkeys/login/finish", origin, payload, c); w.Code != 400 {
		t.Fatal("expired challenge accepted")
	}
	if w := authRequest(s, "POST", "/api/auth/passkeys/register/begin", origin, `{"name":"x"}`); w.Code != 401 {
		t.Fatal("unauthenticated enrollment accepted")
	}
	if w := authRequest(s, "POST", "/api/auth/login", "https://evil.example", `{"password":"secret"}`); w.Code != 403 {
		t.Fatal("foreign origin accepted")
	}
	if w := authRequest(s, "POST", "/api/auth/login", "", `{"password":"secret"}`); w.Code != 403 {
		t.Fatal("missing origin accepted")
	}
	// A localhost credential cannot authenticate on the public RP.
	payload, c = virtualAssertion(t, s, key, "https://video.example.com", "https://video.example.com", 5)
	if w := authRequest(s, "POST", "/api/auth/passkeys/login/finish", "https://video.example.com", payload, c); w.Code != 401 {
		t.Fatal("cross-RP credential accepted")
	}
}
func TestOwnerAuthCompatibilityAndRateLimit(t *testing.T) {
	s := newAuthTestServer(t, "")
	cookie := passwordSession(t, s, "")
	s.db.Exec("UPDATE owner_sessions SET expires=0")
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(cookie)
	if s.isAuthenticated(r) {
		t.Fatal("expired session accepted")
	}
	r = httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "video_server_auth", Value: "old-password-token"})
	if s.isAuthenticated(r) {
		t.Fatal("legacy token accepted")
	}
	s.password = ""
	if s.authEnabled() || !s.isAuthenticated(r) {
		t.Fatal("unauthenticated installation changed")
	}
	s.password = "secret"
	a, _ := s.ownerAuthentication()
	a.attempts = map[string]attemptWindow{}
	for i := 0; i < 20; i++ {
		authRequest(s, "POST", "/api/auth/login", "", `{"password":"wrong"}`)
	}
	if w := authRequest(s, "POST", "/api/auth/login", "", `{"password":"secret"}`); w.Code != 429 {
		t.Fatal("password rate limit absent")
	}
}
func TestAuthOriginConfiguration(t *testing.T) {
	for _, origin := range []string{"http://video.example", "https://127.0.0.1", "https://video.example/path", "https://video.example?x=1", "https://user@video.example"} {
		t.Run(origin, func(t *testing.T) {
			s := newAuthTestServer(t, "")
			s.authOnce = sync.Once{}
			s.authOrigins = origin
			if _, err := s.ownerAuthentication(); err == nil {
				t.Fatal("invalid origin allowed")
			}
		})
	}
	s := newAuthTestServer(t, "")
	s.authOnce = sync.Once{}
	s.authOrigins = "https://video.example"
	s.password = ""
	if _, err := s.ownerAuthentication(); err == nil {
		t.Fatal("bootstrap without password allowed")
	}
}

func TestMultiplePasskeysRemovalAndFreshness(t *testing.T) {
	const origin = "https://video.example.com"
	s := newAuthTestServer(t, origin)
	session := passwordSession(t, s, origin)
	first := enrollVirtual(t, s, origin, session)
	second := enrollVirtual(t, s, origin, session)
	payload, c := virtualAssertion(t, s, second, origin, origin, 5)
	w := authRequest(s, "POST", "/api/auth/passkeys/login/finish", origin, payload, c)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	session = authCookie(t, w, "video_server_auth")
	s.db.Exec("UPDATE owner_sessions SET fresh=?", time.Now().Add(-6*time.Minute).Unix())
	if w = authRequest(s, "DELETE", "/api/auth/passkeys/"+b64(first.id), origin, "", session); w.Code != 403 {
		t.Fatal("stale identity verification accepted")
	}
	s.db.Exec("UPDATE owner_sessions SET fresh=?", time.Now().Unix())
	if w = authRequest(s, "PATCH", "/api/auth/password", origin, `{"enabled":false}`, session); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = authRequest(s, "DELETE", "/api/auth/passkeys/"+b64(first.id), origin, "", session); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	payload, c = virtualAssertion(t, s, first, origin, origin, 5)
	if w = authRequest(s, "POST", "/api/auth/passkeys/login/finish", origin, payload, c); w.Code != 401 {
		t.Fatal("removed passkey accepted")
	}
	w = authRequest(s, "GET", "/api/auth/passkeys", origin, "", session)
	if !strings.Contains(w.Body.String(), "Test device") || strings.Contains(w.Body.String(), b64(first.id)) {
		t.Fatal("credential listing incorrect")
	}
}
func TestOfflineAuthRecovery(t *testing.T) {
	s := newAuthTestServer(t, "https://video.example.com")
	session := passwordSession(t, s, "https://video.example.com")
	enrollVirtual(t, s, "https://video.example.com", session)
	var path string
	var seq int
	var name string
	if err := s.db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIDEO_DB", path)
	t.Setenv("APP_PASSWORD", "")
	if recoverOwnerAuth() == nil {
		t.Fatal("recovery accepted empty password")
	}
	t.Setenv("APP_PASSWORD", "recovered")
	lock, err := lockLibraryDatabase(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if recoverOwnerAuth() == nil {
		t.Fatal("recovery ignored running-server lock")
	}
	lock.Close()
	s.db.Exec("UPDATE owner_auth SET password_enabled=0")
	if err = recoverOwnerAuth(); err != nil {
		t.Fatal(err)
	}
	restarted := &server{db: s.db, password: "recovered", authOrigins: s.authOrigins}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(session)
	if restarted.isAuthenticated(r) {
		t.Fatal("recovery retained session")
	}
	w := authRequest(restarted, "POST", "/api/auth/login", "https://video.example.com", `{"password":"recovered"}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM owner_credentials").Scan(&count)
	if count != 1 {
		t.Fatal("recovery removed credentials")
	}
}
