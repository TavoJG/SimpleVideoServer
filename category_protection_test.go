package main

import (
	"database/sql"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCategoryProtectionLifecycle(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = initDB(db); err != nil {
		t.Fatal(err)
	}
	db.Exec("INSERT INTO categories(name) VALUES('Private')")
	s := &server{db: db, lockedCategories: map[string]string{"private": "legacy"}}
	if err = s.importCategoryProtection(); err != nil {
		t.Fatal(err)
	}
	if !verifyCategoryPassword("legacy", s.categoryPassword("Private")) {
		t.Fatal("legacy import")
	}
	if strings.Contains(s.categoryPassword("Private"), "legacy") {
		t.Fatal("plaintext stored")
	}
	request := func(action, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, httptest.NewRequest("POST", "/api/categories/"+action, strings.NewReader(body)))
		return w
	}
	w := request("unlock", `{"category":"Private","password":"legacy"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	old := w.Result().Cookies()[0]
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(old)
	if !s.canAccessCategory(r, "Private") {
		t.Fatal("unlock failed")
	}
	if w = request("password", `{"category":"Private","current_password":"wrong","password":"new"}`); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w = request("password", `{"category":"Private","current_password":"legacy","password":"new"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if s.canAccessCategory(r, "Private") {
		t.Fatal("stale cookie accepted")
	}
	if w = request("unlock", `{"category":"Private","password":"legacy"}`); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w = request("unlock", `{"category":"Private","password":"new"}`); w.Code != 200 {
		t.Fatal(w.Code)
	}
	restarted := &server{db: db, lockedCategories: s.lockedCategories}
	if err = restarted.importCategoryProtection(); err != nil {
		t.Fatal(err)
	}
	if !verifyCategoryPassword("new", restarted.categoryPassword("Private")) {
		t.Fatal("restart lost password")
	}
	db.Exec("UPDATE categories SET name='Renamed' WHERE name='Private'")
	if !s.isCategoryLocked("Renamed") {
		t.Fatal("rename lost protection")
	}
	if w = request("unhide", `{"category":"Renamed","current_password":"wrong"}`); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w = request("unhide", `{"category":"Renamed","current_password":"new"}`); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if s.isCategoryLocked("Renamed") {
		t.Fatal("unhide failed")
	}
	if w = request("hide", `{"category":"Renamed","password":"again"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	db.Exec("DELETE FROM categories WHERE name='Renamed'")
	if s.isCategoryLocked("Renamed") {
		t.Fatal("delete retained protection")
	}
	if _, err = db.Exec("DROP TABLE category_protection"); err != nil {
		t.Fatal(err)
	}
	if s.canAccessCategory(r, "Renamed") {
		t.Fatal("database failure allowed access")
	}
}

func TestProtectionImportAliasesAndUnhideOverride(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = initDB(db); err != nil {
		t.Fatal(err)
	}
	db.Exec("INSERT INTO categories(name) VALUES('Parent'),('Child'),('Override')")
	s := &server{db: db, lockedCategories: map[string]string{"parent": "parent-secret", "override": "own-secret"}, categoryAliases: map[string]string{"child": "Parent", "override": "Parent"}}
	if err = s.importCategoryProtection(); err != nil {
		t.Fatal(err)
	}
	if !verifyCategoryPassword("parent-secret", s.categoryPassword("Child")) || !verifyCategoryPassword("own-secret", s.categoryPassword("Override")) {
		t.Fatal("inheritance or override failed")
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, httptest.NewRequest("POST", "/api/categories/unhide", strings.NewReader(`{"category":"Parent","current_password":"parent-secret"}`)))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err = s.importCategoryProtection(); err != nil {
		t.Fatal(err)
	}
	if s.isCategoryLocked("Parent") {
		t.Fatal("environment restored removed protection")
	}
	if !s.isCategoryLocked("Child") {
		t.Fatal("child lost independent protection")
	}
}
