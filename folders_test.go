package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func foldersFixture(t *testing.T) (*server, string) {
	t.Helper()
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "folders.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := initDB(db); err != nil {
		t.Fatal(err)
	}
	return &server{db: db, defaultVideoRoot: root}, root
}

func folderFile(t *testing.T, root, relative string) string {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("media"), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func folderCall(handler http.HandlerFunc, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	handler(w, httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body)))
	return w
}

func folderScan(t *testing.T, s *server, root string) {
	t.Helper()
	if _, err := s.scanFolder(root); err != nil {
		t.Fatal(err)
	}
}

func TestFlatCategoryRenamePreservesMediaAndUnindexedContents(t *testing.T) {
	s, root := foldersFixture(t)
	folderFile(t, root, "Trips/clip.mp4")
	notes := folderFile(t, root, "Trips/notes.txt")
	folderScan(t, s, root)
	w := folderCall(s.managedRenameCategory, `{"from":"Trips","to":"Journeys"}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if _, err := os.Stat(notes); !os.IsNotExist(err) {
		t.Fatal("original directory remains")
	}
	if _, err := os.Stat(filepath.Join(root, "Journeys", "notes.txt")); err != nil {
		t.Fatal(err)
	}
	var cat string
	if err := s.db.QueryRow("SELECT category FROM videos WHERE filename='clip.mp4'").Scan(&cat); err != nil || cat != "Journeys" {
		t.Fatal("record not renamed", err)
	}
}
func TestFlatCategoryRenameRollsBackOnDatabaseFailure(t *testing.T) {
	s, root := foldersFixture(t)
	path := folderFile(t, root, "Trips/clip.mp4")
	folderScan(t, s, root)
	s.db.Exec(`CREATE TRIGGER fail_move BEFORE UPDATE ON videos BEGIN SELECT RAISE(ABORT,'injected'); END`)
	w := folderCall(s.managedRenameCategory, `{"from":"Trips","to":"Journeys"}`)
	if w.Code != 500 {
		t.Fatal("expected database failure", w.Body.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("rollback lost file", err)
	}
}
func TestFlatCategoryDeleteRetainsUnindexedAndReportsPartialFailure(t *testing.T) {
	s, root := foldersFixture(t)
	folderFile(t, root, "Trips/good.mp4")
	failed := folderFile(t, root, "Trips/fail.mp4")
	notes := folderFile(t, root, "Trips/notes.txt")
	folderScan(t, s, root)
	s.db.Exec(`CREATE TRIGGER fail_delete BEFORE DELETE ON videos WHEN OLD.filename='fail.mp4' BEGIN SELECT RAISE(ABORT,'injected'); END`)
	result, err := s.deleteFolder("Trips", "")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result)
	if result["deleted"] != 1 || result["folder_retained"] != true {
		t.Fatal(string(data))
	}
	for _, path := range []string{notes, failed} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
}
func TestFlatCategoryLocksAndSymlinks(t *testing.T) {
	s, root := foldersFixture(t)
	folderFile(t, root, "Trips/clip.mp4")
	folderScan(t, s, root)
	s.lockedCategories = parseLockedCategories("Trips=secret")
	if w := folderCall(s.managedRenameCategory, `{"from":"Trips","to":"Journeys"}`); w.Code != 403 {
		t.Fatal("lock not preserved")
	}
	s.lockedCategories = nil
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "Trips", "link")); err != nil {
		t.Fatal(err)
	}
	if w := folderCall(s.managedRenameCategory, `{"from":"Trips","to":"Journeys"}`); w.Code != 400 {
		t.Fatal("symlink renamed")
	}
}
