package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"golang.org/x/crypto/pbkdf2"
	"net/http"
	"strings"
)

func hashCategoryPassword(password string) (string, error) {
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := pbkdf2.Key([]byte(password), salt, 600000, 32, sha256.New)
	return "pbkdf2$" + hex.EncodeToString(salt) + "$" + hex.EncodeToString(key), nil
}
func verifyCategoryPassword(password, credential string) bool {
	if credential == "!unavailable" {
		return false
	}
	if !strings.HasPrefix(credential, "pbkdf2$") {
		return constantStringEqual(password, credential)
	}
	parts := strings.Split(credential, "$")
	if len(parts) != 3 {
		return false
	}
	salt, err := hex.DecodeString(parts[1])
	if err != nil {
		return false
	}
	key := pbkdf2.Key([]byte(password), salt, 600000, 32, sha256.New)
	return constantStringEqual(hex.EncodeToString(key), parts[2])
}

// Import once: later web changes, including unhide, take precedence over environment settings.
func (s *server) importCategoryProtection() error {
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM schema_versions WHERE name='category_protection_v1'").Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	passwords := map[string]string{}
	for name, password := range s.lockedCategories {
		passwords[name] = password
	}
	for name, parent := range s.categoryAliases {
		if passwords[name] == "" {
			passwords[name] = s.lockedCategories[categoryKey(parent)]
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for name, password := range passwords {
		if password == "" {
			continue
		}
		hash, err := hashCategoryPassword(password)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT OR IGNORE INTO category_protection(category,credential) VALUES(?,?)", name, hash); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("INSERT INTO schema_versions(name) VALUES('category_protection_v1')"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *server) manageCategoryProtection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Category        string `json:"category"`
		Password        string `json:"password"`
		CurrentPassword string `json:"current_password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "Invalid JSON body.")
		return
	}
	category, err := normalizeCategory(req.Category)
	if err != nil {
		writeError(w, 400, "Invalid category.")
		return
	}
	s.libraryMu.Lock()
	defer s.libraryMu.Unlock()
	var count int
	if err = s.db.QueryRow("SELECT COUNT(*) FROM categories WHERE name=?", category).Scan(&count); err != nil {
		writeError(w, 500, "Could not load category.")
		return
	}
	if count == 0 {
		writeError(w, 404, "Category not found.")
		return
	}
	action := strings.TrimPrefix(r.URL.Path, "/api/categories/")
	credential := s.categoryPassword(category)
	if credential == "!unavailable" {
		writeError(w, 503, "Category protection unavailable.")
		return
	}
	if action == "hide" {
		if credential != "" {
			writeError(w, 409, "Category is already hidden.")
			return
		}
	} else {
		if credential == "" {
			writeError(w, 400, "Category is not hidden.")
			return
		}
		if !verifyCategoryPassword(req.CurrentPassword, credential) {
			writeError(w, 401, "Invalid current category password.")
			return
		}
	}
	if action == "unhide" {
		_, err = s.db.Exec("DELETE FROM category_protection WHERE category=?", category)
	} else {
		if strings.TrimSpace(req.Password) == "" || len(req.Password) > 1024 {
			writeError(w, 400, "Enter a password of 1–1024 bytes.")
			return
		}
		var hash string
		hash, err = hashCategoryPassword(req.Password)
		if err == nil {
			_, err = s.db.Exec("INSERT INTO category_protection(category,credential) VALUES(?,?) ON CONFLICT(category) DO UPDATE SET credential=excluded.credential", category, hash)
		}
	}
	if err != nil {
		writeError(w, 500, "Could not save category protection.")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: s.categoryCookieName(category), Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil})
	writeJSON(w, 200, map[string]any{"category": category, "hidden": action != "unhide"})
}
