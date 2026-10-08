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
	"strings"
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
	for _, name := range []string{"Travel/clip.jpg", "Travel/Hikes/hike.jpg", "Private/secret.jpg"} {
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

func TestTrashRestorePreservesMetadataAndScanExcludesTrash(t *testing.T) {
	s := libraryFixture(t)
	id := libraryID(t, s, "clip.jpg")
	item, _ := s.queryVideo(id)
	originalCreated := item.CreatedAt
	yes := true
	item, err := s.updateMedia(item, updateVideoRequest{Title: "Trip", Tags: []string{"Summer"}, Favorited: &yes, WatchLater: &yes})
	if err != nil {
		t.Fatal(err)
	}
	results := bulkCall(t, s, bulkRequest{IDs: []int64{id}, Action: "trash"})
	if !results[0].Success {
		t.Fatal(results)
	}
	trashed, _ := s.queryVideo(id)
	if trashed.TrashedAt == "" || trashed.StreamURL != "" || trashed.ThumbnailURL != "" {
		t.Fatalf("invalid trash: %+v", trashed)
	}
	if _, err := os.Stat(trashed.TrashPath); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"/media/", "/thumb/", "/api/videos/"} {
		res := libraryCall(t, s, "GET", route+fmtID(id), nil)
		if res.Code != 404 {
			t.Fatalf("trashed access %s: %d", route, res.Code)
		}
	}
	if err := os.WriteFile(filepath.Join(s.defaultVideoRoot, "Travel", "clip.jpg"), []byte("replacement"), 0o644); err != nil {
		t.Fatal(err)
	}
	scan, err := s.scanFolder(s.defaultVideoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if scan.Found != 3 || scan.Missing != 0 {
		t.Fatalf("scan includes trash: %+v", scan)
	}
	results = bulkCall(t, s, bulkRequest{IDs: []int64{id}, Action: "restore"})
	if !results[0].Success {
		t.Fatal(results)
	}
	restored := results[0].Video
	if restored.ID != id || restored.Title != "Trip" || !restored.Favorited || !restored.WatchLater || restored.CreatedAt != originalCreated || strings.Join(restored.Tags, ",") != "Summer" || restored.Filename != "clip_1.jpg" || restored.TrashedAt != "" {
		t.Fatalf("restored metadata: %+v", restored)
	}
	if data, err := os.ReadFile(restored.Path); err != nil || string(data) != "Travel/clip.jpg" {
		t.Fatalf("restored bytes %q %v", data, err)
	}
}
func fmtID(id int64) string { return strconv.FormatInt(id, 10) }

func TestBulkPartialSuccessAndTagModes(t *testing.T) {
	s := libraryFixture(t)
	id := libraryID(t, s, "clip.jpg")
	locked := libraryID(t, s, "secret.jpg")
	yes := true
	results := bulkCall(t, s, bulkRequest{IDs: []int64{id, locked, 99999}, Action: "favorited", Value: &yes})
	if !results[0].Success || results[1].Success || results[2].Success {
		t.Fatalf("partial results: %+v", results)
	}
	for _, step := range []struct {
		mode string
		tags []string
		want string
	}{{"replace", []string{"Summer", "summer", "Trip"}, "Summer,Trip"}, {"add", []string{"Beach", "SUMMER"}, "Summer,Trip,Beach"}, {"remove", []string{"SUMMER"}, "Trip,Beach"}} {
		r := bulkCall(t, s, bulkRequest{IDs: []int64{id}, Action: "tags", TagMode: step.mode, Tags: step.tags})
		if !r[0].Success || strings.Join(r[0].Video.Tags, ",") != step.want {
			t.Fatalf("tags: %+v", r)
		}
	}
	if res := libraryCall(t, s, "POST", "/api/videos/bulk", bulkRequest{IDs: []int64{id, id}, Action: "trash"}); res.Code != 400 {
		t.Fatal("duplicate IDs accepted")
	}
	cat, sub := "Private", ""
	r := bulkCall(t, s, bulkRequest{IDs: []int64{id}, Action: "move", Category: &cat, Subcategory: &sub})
	if r[0].Success {
		t.Fatal("locked destination accepted")
	}
}

func TestEmptyTrashPreservesLockedItemsAndMissingRestoreFails(t *testing.T) {
	s := libraryFixture(t)
	id := libraryID(t, s, "clip.jpg")
	locked := libraryID(t, s, "secret.jpg")
	for _, vid := range []int64{id, locked} {
		item, _ := s.queryVideo(vid)
		if _, err := s.trashVideo(item); err != nil {
			t.Fatal(err)
		}
	}
	res := libraryCall(t, s, "GET", "/api/trash", nil)
	var items []video
	if err := json.Unmarshal(res.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != id {
		t.Fatalf("locked trash leaked: %+v", items)
	}
	item, _ := s.queryVideo(id)
	if err := os.Remove(item.TrashPath); err != nil {
		t.Fatal(err)
	}
	r := bulkCall(t, s, bulkRequest{IDs: []int64{id}, Action: "restore"})
	if r[0].Success || !strings.Contains(r[0].Error, "missing") {
		t.Fatalf("missing restore: %+v", r)
	}
	res = libraryCall(t, s, "POST", "/api/trash/empty", nil)
	if res.Code != 200 {
		t.Fatal(res.Body.String())
	}
	if _, err := s.queryVideo(id); err != sql.ErrNoRows {
		t.Fatal("accessible trash remains")
	}
	if item, err := s.queryVideo(locked); err != nil || item.TrashedAt == "" {
		t.Fatal("locked trash removed")
	}
}

func TestCategoryTrashPreservesUnindexedContentsAndLocks(t *testing.T) {
	s := libraryFixture(t)
	path := filepath.Join(s.defaultVideoRoot, "Travel", "notes.txt")
	if err := os.WriteFile(path, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := libraryCall(t, s, "POST", "/api/categories/delete", map[string]string{"category": "Travel"})
	if res.Code != 200 || !strings.Contains(res.Body.String(), `"folder_retained":true`) {
		t.Fatalf("folder response: %s", res.Body.String())
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "keep" {
		t.Fatal("unindexed content removed")
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM categories WHERE name='Travel'").Scan(&count); err != nil || count != 1 {
		t.Fatal("restore category lost")
	}
	categories, err := s.queryCategories()
	if err != nil {
		t.Fatal(err)
	}
	for _, cat := range categories {
		if cat.Name == "Travel" && cat.Count != 0 {
			t.Fatal("trashed media counted in active category")
		}
	}
}

func TestMediaMoveAndTrashRollbackOnDatabaseFailure(t *testing.T) {
	s := libraryFixture(t)
	id := libraryID(t, s, "clip.jpg")
	item, _ := s.queryVideo(id)
	if _, err := s.db.Exec(`CREATE TRIGGER reject_update BEFORE UPDATE ON videos BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	cat, sub := "Other", ""
	_, err := s.updateMedia(item, updateVideoRequest{Title: item.Title, Tags: item.Tags, Category: &cat, Subcategory: &sub})
	if err == nil {
		t.Fatal("move did not fail")
	}
	if _, err := os.Stat(item.Path); err != nil {
		t.Fatal("move rollback lost original")
	}
	if _, err := s.trashVideo(item); err == nil {
		t.Fatal("trash did not fail")
	}
	if _, err := os.Stat(item.Path); err != nil {
		t.Fatal("trash rollback lost original")
	}
	if _, err := s.db.Exec("DROP TRIGGER reject_update"); err != nil {
		t.Fatal(err)
	}
	trashed, err := s.trashVideo(item)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_restore BEFORE UPDATE ON videos BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.restoreVideo(trashed, nil, nil); err == nil {
		t.Fatal("restore did not fail")
	}
	if _, err := os.Stat(trashed.TrashPath); err != nil {
		t.Fatal("restore rollback lost trash file")
	}
}

func TestTrashColumnsMigrateExistingDatabase(t *testing.T) {
	s := libraryFixture(t)
	if _, err := s.db.Exec("ALTER TABLE videos DROP COLUMN trashed_at"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("ALTER TABLE videos DROP COLUMN trash_path"); err != nil {
		t.Fatal(err)
	}
	if err := initDB(s.db); err != nil {
		t.Fatal(err)
	}
	if err := initDB(s.db); err != nil {
		t.Fatal(err)
	}
	item, err := s.queryVideo(libraryID(t, s, "clip.jpg"))
	if err != nil || item.TrashedAt != "" {
		t.Fatalf("migration %+v %v", item, err)
	}
}

func TestTrashRequiresAuthentication(t *testing.T) {
	s := libraryFixture(t)
	s.password = "password"
	for _, path := range []string{"/api/trash", "/api/videos/bulk", "/api/trash/empty"} {
		method := http.MethodPost
		if path == "/api/trash" {
			method = http.MethodGet
		}
		if res := libraryCall(t, s, method, path, nil); res.Code != 401 {
			t.Fatalf("unauthenticated %s: %d", path, res.Code)
		}
	}
}

func TestPermanentDeleteRollbackAndExplicitRestoreDestination(t *testing.T) {
	s := libraryFixture(t)
	id := libraryID(t, s, "clip.jpg")
	item, _ := s.queryVideo(id)
	trashed, err := s.trashVideo(item)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_delete BEFORE DELETE ON videos BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.purgeVideo(trashed); err == nil {
		t.Fatal("purge did not fail")
	}
	if _, err := os.Stat(trashed.TrashPath); err != nil {
		t.Fatal("purge rollback lost trash file")
	}
	if _, err := s.queryVideo(id); err != nil {
		t.Fatal("purge rollback lost record")
	}
	if _, err := s.db.Exec("DROP TRIGGER reject_delete"); err != nil {
		t.Fatal(err)
	}
	cat, sub := "Archive", "Trips"
	res := libraryCall(t, s, "POST", "/api/trash/"+fmtID(id)+"/restore", map[string]string{"category": cat, "subcategory": sub})
	if res.Code != 200 {
		t.Fatal(res.Body.String())
	}
	restored, _ := s.queryVideo(id)
	if restored.Category != cat || restored.Subcategory != sub || restored.RelativePath != filepath.Join(cat, sub, item.Filename) {
		t.Fatalf("wrong destination: %+v", restored)
	}
	if r := bulkCall(t, s, bulkRequest{IDs: []int64{id}, Action: "purge"}); r[0].Success {
		t.Fatal("purged active media")
	}
}
