package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestScanAndUpdateVideo(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "videos.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := initDB(db); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "demo.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cover.jpg"), []byte("image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "ignored.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "nested", "deeper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "deeper", "deep.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := &server{db: db}
	result, err := srv.scanFolder(root)
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 3 || result.Found != 3 {
		t.Fatalf("unexpected scan result: %+v", result)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/videos", nil)
	listRes := httptest.NewRecorder()
	srv.listVideos(listRes, listReq)
	if listRes.Code != http.StatusOK {
		t.Fatalf("list status = %d", listRes.Code)
	}

	var videos []video
	if err := json.NewDecoder(listRes.Body).Decode(&videos); err != nil {
		t.Fatal(err)
	}
	if len(videos) != 3 {
		t.Fatalf("unexpected videos: %+v", videos)
	}
	categories := map[string]bool{}
	mediaTypes := map[string]bool{}
	for _, item := range videos {
		categories[item.Category] = true
		mediaTypes[item.MediaType] = true
		if item.ThumbnailURL == "" {
			t.Fatalf("thumbnail url missing: %+v", item)
		}
		if item.MediaType == "image" && item.ThumbnailURL != item.StreamURL {
			t.Fatalf("image thumbnail should use media url: %+v", item)
		}
		if item.MediaType == "video" && item.ThumbnailURL != fmt.Sprintf("/thumb/%d", item.ID) {
			t.Fatalf("video thumbnail should use thumb url: %+v", item)
		}
	}
	if !categories["Uncategorized"] || !categories["nested"] {
		t.Fatalf("unexpected categories: %+v", videos)
	}
	if !mediaTypes["video"] || !mediaTypes["image"] {
		t.Fatalf("unexpected media types: %+v", videos)
	}

	var demoID int64
	for _, item := range videos {
		if item.Filename == "demo.mp4" {
			demoID = item.ID
			break
		}
	}
	if demoID == 0 {
		t.Fatalf("demo.mp4 not found: %+v", videos)
	}

	body := bytes.NewBufferString(`{"title":"Demo Clip","tags":"test, sample, test","category":"moved"}`)
	updateReq := httptest.NewRequest(http.MethodPatch, "/api/videos/1", body)
	updateReq.SetPathValue("id", strconv.FormatInt(demoID, 10))
	updateRes := httptest.NewRecorder()
	srv.updateVideo(updateRes, updateReq)
	if updateRes.Code != http.StatusOK {
		t.Fatalf("update status = %d body = %s", updateRes.Code, updateRes.Body.String())
	}

	var updated video
	if err := json.NewDecoder(updateRes.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.Title != "Demo Clip" || len(updated.Tags) != 2 || updated.Category != "moved" {
		t.Fatalf("unexpected updated video: %+v", updated)
	}
	if _, err := os.Stat(filepath.Join(root, "moved", "demo.mp4")); err != nil {
		t.Fatalf("moved file not found: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "demo.mp4")); !os.IsNotExist(err) {
		t.Fatalf("old file still exists or stat failed unexpectedly: %v", err)
	}
}

func TestPasswordAuthProtectsRoutes(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "videos.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := initDB(db); err != nil {
		t.Fatal(err)
	}

	mux := (&server{db: db, password: "secret"}).routes()

	req := httptest.NewRequest(http.MethodGet, "/api/videos", nil)
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", res.Code)
	}

	loginBody := bytes.NewBufferString(`{"password":"secret"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", loginBody)
	res = httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("login status = %d", res.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/videos", nil)
	for _, cookie := range res.Result().Cookies() {
		req.AddCookie(cookie)
	}
	res = httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("authenticated status = %d", res.Code)
	}
}

func TestFrontendRoutesServeIndex(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "videos.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := initDB(db); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/category/travel/media/42", nil)
	res := httptest.NewRecorder()
	(&server{db: db}).routes().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("frontend route status = %d body = %s", res.Code, res.Body.String())
	}
	if contentType := res.Header().Get("Content-Type"); !strings.Contains(contentType, "text/html") {
		t.Fatalf("frontend route content type = %q", contentType)
	}
}

func TestEmbeddedFrontendAssetsServe(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "videos.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := initDB(db); err != nil {
		t.Fatal(err)
	}

	assets, err := fs.Glob(frontendDist, "frontend/dist/assets/*.js")
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) == 0 {
		t.Fatal("embedded frontend JavaScript asset not found")
	}

	req := httptest.NewRequest(http.MethodGet, strings.TrimPrefix(assets[0], "frontend/dist"), nil)
	res := httptest.NewRecorder()
	(&server{db: db}).routes().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("frontend asset status = %d body = %s", res.Code, res.Body.String())
	}
}

func TestDeleteVideoRemovesFileAndRecord(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "videos.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := initDB(db); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	path := filepath.Join(root, "delete-me.jpg")
	if err := os.WriteFile(path, []byte("image"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := &server{db: db}
	if _, err := srv.scanFolder(root); err != nil {
		t.Fatal(err)
	}

	var id int64
	if err := db.QueryRow("SELECT id FROM videos WHERE filename = ?", "delete-me.jpg").Scan(&id); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/videos/1", nil)
	req.SetPathValue("id", strconv.FormatInt(id, 10))
	res := httptest.NewRecorder()
	srv.deleteVideo(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("delete status = %d body = %s", res.Code, res.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("deleted file still exists or stat failed unexpectedly: %v", err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM videos WHERE id = ?", id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("record was not deleted")
	}
}

func TestDeleteVideoRouteAllowsDeleteMethod(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "videos.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := initDB(db); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	path := filepath.Join(root, "delete-route.mp4")
	if err := os.WriteFile(path, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := &server{db: db}
	if _, err := srv.scanFolder(root); err != nil {
		t.Fatal(err)
	}

	var id int64
	if err := db.QueryRow("SELECT id FROM videos WHERE filename = ?", "delete-route.mp4").Scan(&id); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/videos/"+strconv.FormatInt(id, 10), nil)
	res := httptest.NewRecorder()
	srv.routes().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("route delete status = %d body = %s", res.Code, res.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("deleted file still exists or stat failed unexpectedly: %v", err)
	}
}

func TestDeleteVideoRouteAllowsPostDeleteAction(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "videos.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := initDB(db); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	path := filepath.Join(root, "post-delete-route.mp4")
	if err := os.WriteFile(path, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := &server{db: db}
	if _, err := srv.scanFolder(root); err != nil {
		t.Fatal(err)
	}

	var id int64
	if err := db.QueryRow("SELECT id FROM videos WHERE filename = ?", "post-delete-route.mp4").Scan(&id); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/videos/"+strconv.FormatInt(id, 10)+"/delete", nil)
	res := httptest.NewRecorder()
	srv.routes().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("route post delete status = %d body = %s", res.Code, res.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("deleted file still exists or stat failed unexpectedly: %v", err)
	}
}

func TestRenameCategoryRenamesFolderAndRecords(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "videos.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := initDB(db); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "old", "clip.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "old", "stale.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := &server{db: db}
	if _, err := srv.scanFolder(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "old", "stale.mp4")); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.scanFolder(root); err != nil {
		t.Fatal(err)
	}

	body := bytes.NewBufferString(`{"from":"old","to":"new"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/categories/rename", body)
	res := httptest.NewRecorder()
	srv.routes().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("rename status = %d body = %s", res.Code, res.Body.String())
	}

	if _, err := os.Stat(filepath.Join(root, "new", "clip.mp4")); err != nil {
		t.Fatalf("renamed file not found: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "old")); !os.IsNotExist(err) {
		t.Fatalf("old category folder still exists or stat failed unexpectedly: %v", err)
	}

	var item video
	err = db.QueryRow(
		"SELECT id, path, root, relative_path, category, media_type, filename, title, tags, size_bytes, mtime, missing FROM videos WHERE filename = ?",
		"clip.mp4",
	).Scan(
		&item.ID,
		&item.Path,
		&item.Root,
		&item.RelativePath,
		&item.Category,
		&item.MediaType,
		&item.Filename,
		&item.Title,
		new(string),
		&item.SizeBytes,
		&item.Mtime,
		new(int),
	)
	if err != nil {
		t.Fatal(err)
	}
	if item.Category != "new" || item.RelativePath != filepath.Join("new", "clip.mp4") || item.Path != filepath.Join(root, "new", "clip.mp4") {
		t.Fatalf("record was not renamed: %+v", item)
	}

	var staleCategory, stalePath, staleRelativePath string
	err = db.QueryRow(
		"SELECT category, path, relative_path FROM videos WHERE filename = ?",
		"stale.mp4",
	).Scan(&staleCategory, &stalePath, &staleRelativePath)
	if err != nil {
		t.Fatal(err)
	}
	if staleCategory != "new" || staleRelativePath != filepath.Join("new", "stale.mp4") || stalePath != filepath.Join(root, "new", "stale.mp4") {
		t.Fatalf("missing record was not renamed: category=%q relative_path=%q path=%q", staleCategory, staleRelativePath, stalePath)
	}
}

func TestRenameCategoryRejectsUncategorized(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "videos.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := initDB(db); err != nil {
		t.Fatal(err)
	}

	srv := &server{db: db}
	body := bytes.NewBufferString(`{"from":"Uncategorized","to":"renamed"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/categories/rename", body)
	res := httptest.NewRecorder()
	srv.routes().ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("rename uncategorized status = %d body = %s", res.Code, res.Body.String())
	}
}

func TestRenameCategoryRejectsExistingTargetCategory(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "videos.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := initDB(db); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "old", "old.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "new", "new.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := &server{db: db}
	if _, err := srv.scanFolder(root); err != nil {
		t.Fatal(err)
	}

	body := bytes.NewBufferString(`{"from":"old","to":"new"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/categories/rename", body)
	res := httptest.NewRecorder()
	srv.routes().ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("rename existing target status = %d body = %s", res.Code, res.Body.String())
	}
}

func TestDeleteCategoryDeletesFolderFilesAndRecords(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "videos.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := initDB(db); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "doomed"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "doomed", "clip.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "keep.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := &server{db: db}
	if _, err := srv.scanFolder(root); err != nil {
		t.Fatal(err)
	}

	body := bytes.NewBufferString(`{"category":"doomed"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/categories/delete", body)
	res := httptest.NewRecorder()
	srv.routes().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("delete category status = %d body = %s", res.Code, res.Body.String())
	}

	if _, err := os.Stat(filepath.Join(root, "doomed", "clip.mp4")); !os.IsNotExist(err) {
		t.Fatalf("deleted category file still exists or stat failed unexpectedly: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "doomed")); !os.IsNotExist(err) {
		t.Fatalf("deleted category folder still exists or stat failed unexpectedly: %v", err)
	}

	var deletedCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM videos WHERE category = ?", "doomed").Scan(&deletedCount); err != nil {
		t.Fatal(err)
	}
	if deletedCount != 0 {
		t.Fatalf("deleted category records remain: %d", deletedCount)
	}

	var keepCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM videos WHERE filename = ?", "keep.mp4").Scan(&keepCount); err != nil {
		t.Fatal(err)
	}
	if keepCount != 1 {
		t.Fatalf("unrelated record count = %d", keepCount)
	}
}

func TestDeleteCategoryRejectsUncategorized(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "videos.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := initDB(db); err != nil {
		t.Fatal(err)
	}

	srv := &server{db: db}
	body := bytes.NewBufferString(`{"category":"Uncategorized"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/categories/delete", body)
	res := httptest.NewRecorder()
	srv.routes().ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("delete uncategorized status = %d body = %s", res.Code, res.Body.String())
	}
}

func TestScanUsesConfiguredRootOnly(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "videos.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := initDB(db); err != nil {
		t.Fatal(err)
	}

	configuredRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(configuredRoot, "configured.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	requestedRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(requestedRoot, "requested.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := &server{db: db, defaultVideoRoot: configuredRoot}
	body := bytes.NewBufferString(`{"root":"` + requestedRoot + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/scan", body)
	res := httptest.NewRecorder()
	srv.scan(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("scan status = %d body = %s", res.Code, res.Body.String())
	}

	videos, err := srv.queryVideos("SELECT id, path, root, relative_path, category, media_type, filename, title, tags, size_bytes, mtime, missing FROM videos")
	if err != nil {
		t.Fatal(err)
	}
	if len(videos) != 1 || videos[0].Root != configuredRoot || videos[0].Filename != "configured.mp4" {
		t.Fatalf("scan did not use configured root only: %+v", videos)
	}
}
