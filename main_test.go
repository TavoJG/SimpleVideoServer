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
