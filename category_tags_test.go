package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func recordCategoryMapping(t *testing.T, s *server, parent, sub, target string) {
	t.Helper()
	if _, err := s.db.Exec("INSERT INTO category_migrations(root,parent,subcategory,target,done) VALUES(?,?,?,?,1)", s.defaultVideoRoot, parent, sub, target); err != nil {
		t.Fatal(err)
	}
}
func categoryStoredTags(t *testing.T, s *server, category string) string {
	t.Helper()
	var tags string
	if err := s.db.QueryRow("SELECT tags FROM categories WHERE name=?", category).Scan(&tags); err != nil {
		t.Fatal(err)
	}
	return tags
}
func TestCategoryTagsSeedOnce(t *testing.T) {
	s := libraryFixture(t)
	s.db.Exec("UPDATE categories SET tags=? WHERE name='Hikes'", normalizeTags([]string{"Existing"}))
	recordCategoryMapping(t, s, "Travel", "Hikes", "Hikes")
	recordCategoryMapping(t, s, "travel", "Other", "Hikes")
	recordCategoryMapping(t, s, "Travel", "", "Travel")
	recordCategoryMapping(t, s, "Uncategorized", "Other", "Private")
	if err := initDB(s.db); err != nil {
		t.Fatal(err)
	}
	if tags := categoryStoredTags(t, s, "Hikes"); tags != `["Existing","Travel"]` {
		t.Fatal("seeded tags", tags)
	}
	if tags := categoryStoredTags(t, s, "Travel"); tags != `["Travel"]` {
		t.Fatal("parent not included", tags)
	}
	if tags := categoryStoredTags(t, s, "Private"); tags != "[]" {
		t.Fatal("Uncategorized tag generated")
	}
	response := libraryCall(t, s, "PATCH", "/api/categories/tags", map[string]any{"category": "Hikes", "tags": []string{"Art, Music", "art, music", "Custom"}})
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if err := initDB(s.db); err != nil {
		t.Fatal(err)
	}
	if _, err := s.scanFolder(s.defaultVideoRoot); err != nil {
		t.Fatal(err)
	}
	if tags := categoryStoredTags(t, s, "Hikes"); tags != `["Art, Music","Custom"]` {
		t.Fatal("edit overwritten", tags)
	}
}
func TestCategoryTagsFollowRenameAndIgnoreAbsentMappings(t *testing.T) {
	s := libraryFixture(t)
	recordCategoryMapping(t, s, "Travel", "Hikes", "Hikes")
	recordCategoryMapping(t, s, "Travel", "Missing", "Missing")
	if err := initDB(s.db); err != nil {
		t.Fatal(err)
	}
	response := libraryCall(t, s, "POST", "/api/categories/rename", map[string]string{"from": "Hikes", "to": "Trails"})
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if tags := categoryStoredTags(t, s, "Trails"); tags != `["Travel"]` {
		t.Fatal("rename lost tags", tags)
	}
	response = libraryCall(t, s, "PATCH", "/api/categories/tags", map[string]any{"category": "Trails", "tags": []string{}})
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if err := initDB(s.db); err != nil {
		t.Fatal(err)
	}
	if tags := categoryStoredTags(t, s, "Trails"); tags != "[]" {
		t.Fatal("rename reseeded removed tag", tags)
	}
	var seeded int
	s.db.QueryRow("SELECT COUNT(*) FROM category_tag_seeds WHERE subcategory='Missing'").Scan(&seeded)
	if seeded != 0 {
		t.Fatal("absent category seed marked complete")
	}
	os.Mkdir(filepath.Join(s.defaultVideoRoot, "Missing"), 0755)
	os.WriteFile(filepath.Join(s.defaultVideoRoot, "Missing", "notes.txt"), []byte("keep"), 0600)
	if _, err := s.scanFolder(s.defaultVideoRoot); err != nil {
		t.Fatal(err)
	}
	if tags := categoryStoredTags(t, s, "Missing"); tags != `["Travel"]` {
		t.Fatal("newly discovered category not seeded", tags)
	}
}
func TestCategoryTagsRequireAccessAndHideLockedMetadata(t *testing.T) {
	s := libraryFixture(t)
	s.db.Exec("UPDATE categories SET tags=? WHERE name='Private'", normalizeTags([]string{"Secret"}))
	s.db.Exec("INSERT INTO category_lock_aliases(category,parent) VALUES('Hikes','Private')")
	if err := s.loadCategoryAliases(); err != nil {
		t.Fatal(err)
	}
	s.db.Exec("UPDATE categories SET tags=? WHERE name='Hikes'", normalizeTags([]string{"Protected"}))
	for _, category := range []string{"Private", "Hikes"} {
		res := libraryCall(t, s, "PATCH", "/api/categories/tags", map[string]any{"category": category, "tags": []string{"New"}})
		if res.Code != 403 {
			t.Fatal("locked edit allowed", res.Code)
		}
	}
	res := libraryCall(t, s, "GET", "/api/categories", nil)
	if strings.Contains(res.Body.String(), "Secret") || strings.Contains(res.Body.String(), "Protected") {
		t.Fatal("locked tags leaked", res.Body.String())
	}
	body := bytes.NewBufferString(`{"category":"Hikes","tags":["Accessible"]}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/categories/tags", body)
	req.AddCookie(&http.Cookie{Name: s.categoryCookieName("Hikes"), Value: s.categoryToken("Hikes")})
	recorder := httptest.NewRecorder()
	s.routes().ServeHTTP(recorder, req)
	if recorder.Code != 200 {
		t.Fatal("unlocked edit rejected", recorder.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/categories", nil)
	req.AddCookie(&http.Cookie{Name: s.categoryCookieName("Hikes"), Value: s.categoryToken("Hikes")})
	recorder = httptest.NewRecorder()
	s.routes().ServeHTTP(recorder, req)
	if !strings.Contains(recorder.Body.String(), "Accessible") {
		t.Fatal("unlocked tags absent")
	}
	s.password = "password"
	if res := libraryCall(t, s, "PATCH", "/api/categories/tags", map[string]any{"category": "Travel", "tags": []string{}}); res.Code != 401 {
		t.Fatal("unauthenticated edit allowed")
	}
}
func TestCategoryTagsRollbackAndValidateRequests(t *testing.T) {
	s := libraryFixture(t)
	recordCategoryMapping(t, s, "Travel", "Hikes", "Hikes")
	if _, err := s.db.Exec(`CREATE TRIGGER reject_category_tags BEFORE UPDATE OF tags ON categories BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if err := syncCategories(s.db); err == nil {
		t.Fatal("expected seeding failure")
	}
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM category_tag_seeds").Scan(&count)
	if count != 0 {
		t.Fatal("failed seed marked complete")
	}
	if tags := categoryStoredTags(t, s, "Hikes"); tags != "[]" {
		t.Fatal("rollback changed tags")
	}
	res := libraryCall(t, s, "PATCH", "/api/categories/tags", map[string]any{"category": "Travel", "tags": []string{"New"}})
	if res.Code != 500 {
		t.Fatal("database failure hidden", res.Code)
	}
	s.db.Exec("DROP TRIGGER reject_category_tags")
	if err := syncCategories(s.db); err != nil {
		t.Fatal(err)
	}
	for _, body := range []any{map[string]any{"category": "Travel"}, map[string]any{"category": "Travel", "tags": nil}, map[string]any{"category": "Travel", "tags": "string"}, map[string]any{"category": "../outside", "tags": []string{}}, map[string]any{"category": "Travel", "tags": []string{"bad\x00tag"}}} {
		if res := libraryCall(t, s, "PATCH", "/api/categories/tags", body); res.Code != 400 {
			t.Fatal("invalid request accepted", body, res.Code)
		}
	}
	if res := libraryCall(t, s, "PATCH", "/api/categories/tags", map[string]any{"category": "Absent", "tags": []string{}}); res.Code != 404 {
		t.Fatal("absent category accepted")
	}
	if res := libraryCall(t, s, "PATCH", "/api/categories/tags", map[string]any{"category": "Uncategorized", "tags": []string{"Root"}}); res.Code != 200 {
		t.Fatal("root editing rejected", res.Body.String())
	}
	cats, err := s.queryCategories()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(cats)
	if !strings.Contains(string(encoded), `"tags":["Root"]`) {
		t.Fatal("root tags not returned")
	}
}
