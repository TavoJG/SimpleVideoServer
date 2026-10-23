package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

type ownerAuth struct {
	id, secret []byte
	clients    map[string]*webauthn.WebAuthn
	mu         sync.Mutex
	pending    map[string]authChallenge
	attempts   map[string]attemptWindow
}
type authChallenge struct {
	data                             webauthn.SessionData
	origin, operation, session, name string
	expires                          time.Time
}
type attemptWindow struct {
	count   int
	expires time.Time
}
type ownerSession struct {
	token, origin string
	fresh         int64
	passkey       bool
}
type ownerUser struct {
	id          []byte
	credentials []webauthn.Credential
}

func (u ownerUser) WebAuthnID() []byte                         { return u.id }
func (u ownerUser) WebAuthnName() string                       { return "owner" }
func (u ownerUser) WebAuthnDisplayName() string                { return "Video Library owner" }
func (u ownerUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }
func randomAuthToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func (s *server) ownerAuthentication() (*ownerAuth, error) {
	s.authOnce.Do(func() { s.auth, s.authErr = s.initOwnerAuthentication() })
	return s.auth, s.authErr
}
func (s *server) initOwnerAuthentication() (*ownerAuth, error) {
	if s.db == nil {
		return nil, errors.New("authentication requires database")
	}
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS owner_auth(id INTEGER PRIMARY KEY CHECK(id=1),owner_id BLOB NOT NULL,secret BLOB NOT NULL,password_enabled INTEGER NOT NULL DEFAULT 1)`,
		`CREATE TABLE IF NOT EXISTS owner_credentials(id TEXT PRIMARY KEY,rp_id TEXT NOT NULL,name TEXT NOT NULL,credential BLOB NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS owner_sessions(token TEXT PRIMARY KEY,expires INTEGER NOT NULL,fresh INTEGER NOT NULL,origin TEXT NOT NULL,passkey INTEGER NOT NULL)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			return nil, err
		}
	}
	a := &ownerAuth{clients: map[string]*webauthn.WebAuthn{}, pending: map[string]authChallenge{}, attempts: map[string]attemptWindow{}}
	if _, err := s.db.Exec("INSERT OR IGNORE INTO owner_auth(id,owner_id,secret) VALUES(1,?,?)", []byte(randomAuthToken()), []byte(randomAuthToken())); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow("SELECT owner_id,secret FROM owner_auth WHERE id=1").Scan(&a.id, &a.secret); err != nil {
		return nil, err
	}
	for _, raw := range strings.Split(s.authOrigins, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || (u.Scheme != "https" && !(u.Scheme == "http" && u.Hostname() == "localhost")) || net.ParseIP(u.Hostname()) != nil {
			return nil, fmt.Errorf("invalid APP_AUTH_ORIGINS entry %q: use an exact HTTPS origin or http://localhost:port", raw)
		}
		required := true
		client, err := webauthn.New(&webauthn.Config{RPID: u.Hostname(), RPDisplayName: "Video Library", RPOrigins: []string{raw}, AuthenticatorSelection: protocol.AuthenticatorSelection{RequireResidentKey: &required, ResidentKey: protocol.ResidentKeyRequirementRequired, UserVerification: protocol.VerificationRequired}})
		if err != nil {
			return nil, err
		}
		a.clients[raw] = client
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM owner_credentials").Scan(&count); err != nil {
		return nil, err
	}
	if len(a.clients) > 0 && count == 0 && s.password == "" {
		return nil, errors.New("APP_PASSWORD is required for initial passkey enrollment")
	}
	return a, nil
}
func (a *ownerAuth) passwordAllowed(s *server) bool {
	var enabled bool
	return s.password != "" && s.db.QueryRow("SELECT password_enabled FROM owner_auth WHERE id=1").Scan(&enabled) == nil && enabled
}
func (a *ownerAuth) clientForPage(r *http.Request) *webauthn.WebAuthn {
	for origin, c := range a.clients {
		u, _ := url.Parse(origin)
		if u.Host == r.Host {
			return c
		}
	}
	return nil
}
func (a *ownerAuth) validMutation(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if len(a.clients) > 0 {
		return a.clients[origin] != nil
	}
	if origin == "" {
		return r.Header.Get("Sec-Fetch-Site") != "cross-site"
	}
	u, e := url.Parse(origin)
	return e == nil && u.Host == r.Host && (u.Scheme == "http" || u.Scheme == "https")
}
func (a *ownerAuth) secure(r *http.Request) bool {
	if o := r.Header.Get("Origin"); a.clients[o] != nil {
		return strings.HasPrefix(o, "https://")
	}
	for o := range a.clients {
		u, _ := url.Parse(o)
		if u.Host == r.Host && u.Scheme == "https" {
			return true
		}
	}
	return r.TLS != nil
}
func (a *ownerAuth) clearCookie(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: a.secure(r), SameSite: http.SameSiteLaxMode})
}
func (a *ownerAuth) sign(token string) string {
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}
func (a *ownerAuth) session(s *server, r *http.Request) *ownerSession {
	c, err := r.Cookie("video_server_auth")
	if err != nil {
		return nil
	}
	ss := &ownerSession{token: a.sign(c.Value)}
	if s.db.QueryRow("SELECT fresh,origin,passkey FROM owner_sessions WHERE token=? AND expires>?", ss.token, time.Now().Unix()).Scan(&ss.fresh, &ss.origin, &ss.passkey) != nil {
		return nil
	}
	return ss
}
func (a *ownerAuth) issueSession(s *server, w http.ResponseWriter, r *http.Request, passkey bool) error {
	token := randomAuthToken()
	now := time.Now()
	// Authentication rotates the browser session, including when used for fresh verification.
	if old := a.session(s, r); old != nil {
		if _, err := s.db.Exec("DELETE FROM owner_sessions WHERE token=?", old.token); err != nil {
			return err
		}
	}
	if _, err := s.db.Exec("DELETE FROM owner_sessions WHERE expires<=?", now.Unix()); err != nil {
		return err
	}
	if _, err := s.db.Exec("INSERT INTO owner_sessions VALUES(?,?,?,?,?)", a.sign(token), now.Add(30*24*time.Hour).Unix(), now.Unix(), r.Header.Get("Origin"), passkey); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: "video_server_auth", Value: token, Path: "/", MaxAge: 30 * 24 * 60 * 60, HttpOnly: true, Secure: a.secure(r), SameSite: http.SameSiteLaxMode})
	return nil
}
func (a *ownerAuth) allow(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for k, v := range a.attempts {
		if now.After(v.expires) {
			delete(a.attempts, k)
		}
	}
	// A global cap also bounds memory and protects deployments behind a reverse proxy.
	for _, key := range []string{"*", "ip:" + host} {
		v := a.attempts[key]
		limit := 20
		if key == "*" {
			limit = 200
		}
		if v.count >= limit {
			return false
		}
	}
	if len(a.attempts) > 4096 {
		return false
	}
	for _, key := range []string{"*", "ip:" + host} {
		v := a.attempts[key]
		if v.count == 0 {
			v.expires = now.Add(time.Minute)
		}
		v.count++
		a.attempts[key] = v
	}
	return true
}
func (a *ownerAuth) user(s *server, rp string) (ownerUser, error) {
	u := ownerUser{id: a.id}
	rows, err := s.db.Query("SELECT credential FROM owner_credentials WHERE rp_id=?", rp)
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var c webauthn.Credential
		if err = rows.Scan(&raw); err != nil {
			return u, err
		}
		if err = json.Unmarshal(raw, &c); err != nil {
			return u, err
		}
		u.credentials = append(u.credentials, c)
	}
	return u, rows.Err()
}
func (s *server) passkeyRoutes(mux *http.ServeMux) {
	for _, op := range []string{"register", "login"} {
		for _, step := range []string{"begin", "finish"} {
			mux.HandleFunc("POST /api/auth/passkeys/"+op+"/"+step, s.passkeyCeremony)
		}
	}
	mux.HandleFunc("GET /api/auth/passkeys", s.securitySettings)
	mux.HandleFunc("DELETE /api/auth/passkeys/{id}", s.securitySettings)
	mux.HandleFunc("PATCH /api/auth/password", s.securitySettings)
}
func (s *server) passkeyCeremony(w http.ResponseWriter, r *http.Request) {
	a, err := s.ownerAuthentication()
	if err != nil {
		writeError(w, 500, "Authentication unavailable.")
		return
	}
	origin := r.Header.Get("Origin")
	client := a.clients[origin]
	if client == nil {
		writeError(w, 403, "Passkeys are not configured for this origin.")
		return
	}
	register := strings.Contains(r.URL.Path, "/register/")
	op := "login"
	session := ""
	if register {
		op = "register"
		ss := a.session(s, r)
		if ss == nil {
			writeError(w, 401, "Sign in before adding a passkey.")
			return
		}
		session = ss.token
	}
	if strings.HasSuffix(r.URL.Path, "/begin") {
		if !a.allow(r) {
			writeError(w, 429, "Too many attempts. Try again later.")
			return
		}
		var options any
		var data *webauthn.SessionData
		name := ""
		if register {
			var req struct {
				Name string `json:"name"`
			}
			if readJSON(r, &req) != nil || len(strings.TrimSpace(req.Name)) == 0 || len(req.Name) > 100 {
				writeError(w, 400, "Enter a passkey name of 1–100 bytes.")
				return
			}
			name = strings.TrimSpace(req.Name)
			u, e := a.user(s, client.Config.RPID)
			if e != nil {
				writeError(w, 500, "Could not load credentials.")
				return
			}
			excluded := []protocol.CredentialDescriptor{}
			for _, c := range u.credentials {
				excluded = append(excluded, c.Descriptor())
			}
			options, data, err = client.BeginRegistration(u, webauthn.WithExclusions(excluded))
		} else {
			options, data, err = client.BeginDiscoverableLogin()
		}
		if err != nil {
			writeError(w, 500, "Could not start passkey verification.")
			return
		}
		token := randomAuthToken()
		a.mu.Lock()
		for k, v := range a.pending {
			if time.Now().After(v.expires) {
				delete(a.pending, k)
			}
		}
		if len(a.pending) >= 1000 {
			a.mu.Unlock()
			writeError(w, 429, "Too many pending requests.")
			return
		}
		a.pending[a.sign(token)] = authChallenge{data: *data, origin: origin, operation: op, session: session, name: name, expires: time.Now().Add(5 * time.Minute)}
		a.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "video_server_ceremony", Value: token, Path: "/api/auth/passkeys", MaxAge: 300, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: a.secure(r)})
		writeJSON(w, 200, options)
		return
	}
	c, e := r.Cookie("video_server_ceremony")
	if e != nil {
		writeError(w, 400, "Passkey request expired. Try again.")
		return
	}
	a.mu.Lock()
	challenge, ok := a.pending[a.sign(c.Value)]
	delete(a.pending, a.sign(c.Value))
	a.mu.Unlock()
	if !ok || time.Now().After(challenge.expires) || challenge.origin != origin || challenge.operation != op || challenge.session != session {
		writeError(w, 400, "Passkey request expired or mismatched. Try again.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	// Credential removal and session revocation must not race a completed login.
	a.mu.Lock()
	defer a.mu.Unlock()
	if register && a.session(s, r) == nil {
		writeError(w, 401, "Sign in before adding a passkey.")
		return
	}
	var credential *webauthn.Credential
	u, e := a.user(s, client.Config.RPID)
	if e != nil {
		writeError(w, 500, "Could not load credentials.")
		return
	}
	if register {
		credential, err = client.FinishRegistration(u, challenge.data, r)
	} else {
		credential, err = client.FinishDiscoverableLogin(func(rawID, userHandle []byte) (webauthn.User, error) {
			if !bytes.Equal(userHandle, a.id) {
				return nil, errors.New("unknown owner")
			}
			for _, c := range u.credentials {
				if bytes.Equal(c.ID, rawID) {
					return u, nil
				}
			}
			return nil, errors.New("unknown credential")
		}, challenge.data, r)
	}
	if err != nil {
		writeError(w, 401, "Passkey verification failed. Try again.")
		return
	}
	raw, err := json.Marshal(credential)
	if err != nil {
		writeError(w, 500, "Could not save passkey.")
		return
	}
	id := base64.RawURLEncoding.EncodeToString(credential.ID)
	if register {
		_, err = s.db.Exec("INSERT INTO owner_credentials VALUES(?,?,?,?)", id, client.Config.RPID, challenge.name, raw)
	} else {
		_, err = s.db.Exec("UPDATE owner_credentials SET credential=? WHERE id=? AND rp_id=?", raw, id, client.Config.RPID)
	}
	if err != nil {
		writeError(w, 500, "Could not save passkey.")
		return
	}
	if !register {
		if err = a.issueSession(s, w, r, true); err != nil {
			writeError(w, 500, "Could not create session.")
			return
		}
	}
	writeJSON(w, 200, map[string]bool{"authenticated": true})
}
func (s *server) securitySettings(w http.ResponseWriter, r *http.Request) {
	a, err := s.ownerAuthentication()
	if err != nil {
		writeError(w, 500, "Authentication unavailable.")
		return
	}
	ss := a.session(s, r)
	if ss == nil {
		writeError(w, 401, "Authentication required.")
		return
	}
	if r.Method == "GET" {
		rows, e := s.db.Query("SELECT id,rp_id,name FROM owner_credentials ORDER BY rp_id,name")
		if e != nil {
			writeError(w, 500, "Could not load credentials.")
			return
		}
		defer rows.Close()
		items := []map[string]string{}
		for rows.Next() {
			var id, rp, name string
			if rows.Scan(&id, &rp, &name) != nil {
				writeError(w, 500, "Could not load credentials.")
				return
			}
			items = append(items, map[string]string{"id": id, "hostname": rp, "name": name})
		}
		if rows.Err() != nil {
			writeError(w, 500, "Could not load credentials.")
			return
		}
		writeJSON(w, 200, map[string]any{"credentials": items, "password_enabled": a.passwordAllowed(s)})
		return
	}
	if !a.validMutation(r) {
		writeError(w, 403, "Origin not allowed.")
		return
	}
	if ss.fresh < time.Now().Add(-5*time.Minute).Unix() || ss.origin != r.Header.Get("Origin") {
		writeError(w, 403, "Verify your identity again before changing security settings.")
		return
	}
	// Serialize policy and removal checks in one transaction to prevent last-key races.
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.session(s, r) == nil {
		writeError(w, 401, "Authentication required.")
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, 500, "Could not update security settings.")
		return
	}
	defer tx.Rollback()
	var enabled bool
	var count int
	if tx.QueryRow("SELECT password_enabled FROM owner_auth WHERE id=1").Scan(&enabled) != nil || tx.QueryRow("SELECT COUNT(*) FROM owner_credentials").Scan(&count) != nil {
		writeError(w, 500, "Could not load security settings.")
		return
	}
	if r.Method == "DELETE" {
		if count <= 1 && (!enabled || s.password == "") {
			writeError(w, 409, "Keep at least one passkey while password login is unavailable.")
			return
		}
		_, err = tx.Exec("DELETE FROM owner_credentials WHERE id=?", r.PathValue("id"))
	} else {
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if readJSON(r, &req) != nil {
			writeError(w, 400, "Invalid JSON body.")
			return
		}
		if !req.Enabled && (!ss.passkey || count == 0) {
			writeError(w, 403, "Sign in with a passkey on this hostname before disabling passwords.")
			return
		}
		if req.Enabled && s.password == "" {
			writeError(w, 409, "Configure APP_PASSWORD before enabling password login.")
			return
		}
		_, err = tx.Exec("UPDATE owner_auth SET password_enabled=? WHERE id=1", req.Enabled)
	}
	// Revoke other sessions so removed credentials cannot retain library access.
	if err == nil {
		_, err = tx.Exec("DELETE FROM owner_sessions WHERE token<>?", ss.token)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		writeError(w, 500, "Could not update security settings.")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func recoverOwnerAuth() error {
	password := strings.TrimSpace(os.Getenv("APP_PASSWORD"))
	if password == "" {
		return errors.New("set a nonempty APP_PASSWORD before recovery")
	}
	path := envOrDefault("VIDEO_DB", "videos.sqlite3")
	lock, err := lockLibraryDatabase(path, false)
	if err != nil {
		return err
	}
	defer lock.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	s := &server{db: db, password: password}
	if _, err = s.ownerAuthentication(); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE owner_auth SET password_enabled=1,secret=? WHERE id=1", []byte(randomAuthToken())); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM owner_sessions"); err != nil {
		return err
	}
	if err = tx.Commit(); err == nil {
		fmt.Println("Password login restored; sessions invalidated. Restart the server with APP_PASSWORD set.")
	}
	return err
}
