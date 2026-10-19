package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func libraryFixture(t *testing.T) *server {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := initDB(db); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, name := range []string{"Travel/clip.jpg", "Hikes/hike.jpg", "Private/secret.jpg"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := &server{db: db, defaultVideoRoot: root, lockedCategories: parseLockedCategories("Private=secret")}
	if _, err := s.scanFolder(root); err != nil {
		t.Fatal(err)
	}
	return s
}
func libraryID(t *testing.T, s *server, name string) int64 {
	t.Helper()
	var id int64
	if err := s.db.QueryRow("SELECT id FROM videos WHERE filename=?", name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
func libraryCall(t *testing.T, s *server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	s.routes().ServeHTTP(res, httptest.NewRequest(method, path, bytes.NewReader(data)))
	return res
}
func bulkCall(t *testing.T, s *server, req bulkRequest) []bulkItemResult {
	t.Helper()
	res := libraryCall(t, s, "POST", "/api/videos/bulk", req)
	if res.Code != 200 {
		t.Fatalf("bulk status %d: %s", res.Code, res.Body.String())
	}
	var result struct {
		Results []bulkItemResult `json:"results"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Results
}

func TestBulkPartialSuccess(t *testing.T) {
	s := libraryFixture(t)
	id := libraryID(t, s, "clip.jpg")
	locked := libraryID(t, s, "secret.jpg")
	yes := true
	results := bulkCall(t, s, bulkRequest{IDs: []int64{id, locked, 99999}, Action: "favorited", Value: &yes})
	if !results[0].Success || results[1].Success || results[2].Success {
		t.Fatalf("partial results: %+v", results)
	}
	if res := libraryCall(t, s, "POST", "/api/videos/bulk", bulkRequest{IDs: []int64{id, id}, Action: "delete"}); res.Code != 400 {
		t.Fatal("duplicate IDs accepted")
	}
	cat := "Private"
	r := bulkCall(t, s, bulkRequest{IDs: []int64{id}, Action: "move", Category: &cat})
	if r[0].Success {
		t.Fatal("locked destination accepted")
	}
}

func fmtID(id int64) string { return strconv.FormatInt(id, 10) }

func TestPermanentDeletionRollbackLocksAndRemovedEndpoints(t *testing.T) {
	s := libraryFixture(t)
	id := libraryID(t, s, "clip.jpg")
	item, _ := s.queryVideo(id)
	if _, err := s.db.Exec(`CREATE TRIGGER reject_delete BEFORE DELETE ON videos BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.permanentDelete(item); err == nil {
		t.Fatal("expected deletion failure")
	}
	if data, err := os.ReadFile(item.Path); err != nil || string(data) != "Travel/clip.jpg" {
		t.Fatalf("file lost: %q %v", data, err)
	}
	if _, err := s.queryVideo(id); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM pending_deletions").Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("journal rollback %d %v", pending, err)
	}
	s.db.Exec("DROP TRIGGER reject_delete")
	locked := libraryID(t, s, "secret.jpg")
	results := bulkCall(t, s, bulkRequest{IDs: []int64{id, locked, 99999}, Action: "delete"})
	if !results[0].Success || results[0].Video != nil || results[1].Success || results[2].Success {
		t.Fatalf("results: %+v", results)
	}
	if _, err := s.queryVideo(id); err != sql.ErrNoRows {
		t.Fatal("record retained", err)
	}
	if _, err := os.Stat(item.Path); !os.IsNotExist(err) {
		t.Fatal("file retained", err)
	}
	for _, path := range []string{"/api/trash", "/api/trash/empty", "/api/trash/1/restore"} {
		method := http.MethodPost
		if path == "/api/trash" {
			method = http.MethodGet
		}
		if res := libraryCall(t, s, method, path, nil); res.Code != 404 {
			t.Fatalf("removed route %s: %d", path, res.Code)
		}
	}
	for _, action := range []string{"trash", "restore", "purge"} {
		if res := libraryCall(t, s, "POST", "/api/videos/bulk", bulkRequest{IDs: []int64{locked}, Action: action}); res.Code != 400 {
			t.Fatalf("old action %s: %d", action, res.Code)
		}
	}
	s.password = "password"
	if res := libraryCall(t, s, "POST", "/api/videos/bulk", bulkRequest{IDs: []int64{locked}, Action: "delete"}); res.Code != 401 {
		t.Fatal("unauthenticated deletion allowed")
	}
}

func TestDeletionRejectsSymlinksAndDeletesMissingRecord(t *testing.T) {
	s := libraryFixture(t)
	item, _ := s.queryVideo(libraryID(t, s, "clip.jpg"))
	outside := filepath.Join(t.TempDir(), "outside.jpg")
	os.WriteFile(outside, []byte("keep"), 0600)
	os.Remove(item.Path)
	if err := os.Symlink(outside, item.Path); err != nil {
		t.Fatal(err)
	}
	if err := s.permanentDelete(item); err == nil {
		t.Fatal("symlink deleted")
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "keep" {
		t.Fatal("external file changed")
	}
	os.Remove(item.Path)
	if err := s.permanentDelete(item); err != nil {
		t.Fatal(err)
	}
}
