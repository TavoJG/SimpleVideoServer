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

func assertManagedDirectoryMode(t *testing.T, path string) {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", path)
	}
	if info.Mode().Perm() != 0o775 {
		t.Fatalf("directory %s permissions = %#o, want %#o", path, info.Mode().Perm(), os.FileMode(0o775))
	}
}

func categoryNames(t *testing.T, db *sql.DB) []string {
	t.Helper()

	rows, err := db.Query("SELECT name FROM categories ORDER BY name COLLATE NOCASE ASC")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return names
}

func TestStorageForPath(t *testing.T) {
	usage, err := storageForPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if usage.TotalBytes == 0 {
		t.Fatal("total storage must be reported")
	}
	if usage.UsedBytes > usage.TotalBytes {
		t.Fatalf("used storage %d exceeds total storage %d", usage.UsedBytes, usage.TotalBytes)
	}
}

func TestScanReportsMissingFiles(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "videos.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := initDB(db); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "demo.mp4")
	if err := os.WriteFile(path, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &server{db: db}
	first, err := srv.scanFolder(root)
	if err != nil {
		t.Fatal(err)
	}
	if first.Root != root || first.Found != 1 || first.Added != 1 || first.Updated != 0 || first.Missing != 0 || first.LastScanAt == "" {
		t.Fatalf("unexpected scan: %+v", first)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	second, err := srv.scanFolder(root)
	if err != nil {
		t.Fatal(err)
	}
	if second.Missing != 1 || second.Found != 0 {
		t.Fatalf("unexpected missing scan: %+v", second)
	}
	encoded, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	var legacy struct {
		Root                  string
		Found, Added, Updated int
	}
	if err := json.Unmarshal(encoded, &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Root != root || legacy.Found != 0 {
		t.Fatalf("legacy response: %+v", legacy)
	}
}

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
	if result.Added != 4 || result.Found != 4 {
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
	if len(videos) != 4 {
		t.Fatalf("unexpected videos: %+v", videos)
	}
	categories := map[string]bool{}
	mediaTypes := map[string]bool{}
	var deepVideo video
	for _, item := range videos {
		categories[item.Category] = true
		mediaTypes[item.MediaType] = true
		if item.Filename == "deep.mp4" {
			deepVideo = item
		}
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
	if deepVideo.Subcategory != "deeper" {
		t.Fatalf("expected deep.mp4 to be indexed under subcategory: %+v", deepVideo)
	}
	if got := strings.Join(categoryNames(t, db), ","); got != "nested,Uncategorized" {
		t.Fatalf("unexpected category records after scan: %s", got)
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

	body := bytes.NewBufferString(`{"title":"Demo Clip","tags":"test, sample, test","category":"moved","subcategory":"clips"}`)
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
	if updated.Title != "Demo Clip" || len(updated.Tags) != 2 || updated.Category != "moved" || updated.Subcategory != "clips" {
		t.Fatalf("unexpected updated video: %+v", updated)
	}
	if _, err := os.Stat(filepath.Join(root, "moved", "clips", "demo.mp4")); err != nil {
		t.Fatalf("moved file not found: %v", err)
	}
	assertManagedDirectoryMode(t, filepath.Join(root, "moved"))
	assertManagedDirectoryMode(t, filepath.Join(root, "moved", "clips"))
	if _, err := os.Stat(filepath.Join(root, "demo.mp4")); !os.IsNotExist(err) {
		t.Fatalf("old file still exists or stat failed unexpectedly: %v", err)
	}
	if got := strings.Join(categoryNames(t, db), ","); got != "moved,nested,Uncategorized" {
		t.Fatalf("unexpected category records after move: %s", got)
	}
}

func TestVideoStateDefaultsAndUpdates(t *testing.T) {
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

	srv := &server{db: db}
	if _, err := srv.scanFolder(root); err != nil {
		t.Fatal(err)
	}

	videos, err := srv.queryVideos(videoSelectSQL())
	if err != nil {
		t.Fatal(err)
	}
	if len(videos) != 1 {
		t.Fatalf("unexpected videos: %+v", videos)
	}
	item := videos[0]
	if item.CreatedAt == "" || item.Favorited || item.WatchLater {
		t.Fatalf("unexpected default state: %+v", item)
	}

	body := bytes.NewBufferString(`{"title":"Demo","tags":[],"favorited":true,"watch_later":true}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/videos/1", body)
	req.SetPathValue("id", strconv.FormatInt(item.ID, 10))
	res := httptest.NewRecorder()
	srv.updateVideo(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("update status = %d body = %s", res.Code, res.Body.String())
	}

	var updated video
	if err := json.NewDecoder(res.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if !updated.Favorited || !updated.WatchLater {
		t.Fatalf("favorite/watch later did not update: %+v", updated)
	}
}

func TestInitDBRemovesLegacyPlaybackColumns(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "videos.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := initDB(db); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"ALTER TABLE videos ADD COLUMN playback_position_seconds REAL DEFAULT 0",
		"ALTER TABLE videos ADD COLUMN duration_seconds REAL DEFAULT 0",
		"ALTER TABLE videos ADD COLUMN last_played_at TEXT",
		"ALTER TABLE categories ADD COLUMN last_reproduced TEXT",
		`INSERT INTO videos (path, root, relative_path, filename, title, tags, favorited, watch_later, created_at, playback_position_seconds) VALUES ('/demo.mp4', '/', 'demo.mp4', 'demo.mp4', 'Demo', 'tag', 1, 1, '2026-01-01', 42)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := initDB(db); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('videos') WHERE name IN ('playback_position_seconds', 'duration_seconds', 'last_played_at')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("legacy video columns = %d", count)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('categories') WHERE name = 'last_reproduced'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("legacy category column remains")
	}
	item, err := (&server{db: db}).queryVideo(1)
	if err != nil {
		t.Fatal(err)
	}
	if item.Title != "Demo" || strings.Join(item.Tags, ",") != "tag" || !item.Favorited || !item.WatchLater || item.CreatedAt != "2026-01-01" {
		t.Fatalf("metadata changed: %+v", item)
	}
}

func TestPlaybackEndpointRemoved(t *testing.T) {
	srv := &server{}
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		res := httptest.NewRecorder()
		srv.videoItem(res, httptest.NewRequest(method, "/api/videos/1/playback", nil))
		if res.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", res.Code)
		}
	}
}

func TestListCategoriesReturnsCounts(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "videos.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := initDB(db); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "uncategorized.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "travel"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "travel", "trip.jpg"), []byte("image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "travel", "europe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "travel", "europe", "paris.jpg"), []byte("image"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := &server{db: db}
	if _, err := srv.scanFolder(root); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/categories", nil)
	res := httptest.NewRecorder()
	srv.listCategories(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("list categories status = %d", res.Code)
	}

	var categories []categorySummary
	if err := json.NewDecoder(res.Body).Decode(&categories); err != nil {
		t.Fatal(err)
	}
	if len(categories) != 2 {
		t.Fatalf("unexpected categories: %+v", categories)
	}
	if categories[0].Name != "travel" || categories[0].Count != 2 {
		t.Fatalf("unexpected first category: %+v", categories[0])
	}
	if categories[0].DirectCount != 1 || len(categories[0].Subcategories) != 1 {
		t.Fatalf("unexpected travel category nesting: %+v", categories[0])
	}
	if categories[0].Subcategories[0].Name != "europe" || categories[0].Subcategories[0].Count != 1 {
		t.Fatalf("unexpected subcategory summary: %+v", categories[0].Subcategories)
	}
	if categories[1].Name != "Uncategorized" || categories[1].Count != 1 {
		t.Fatalf("unexpected second category: %+v", categories[1])
	}
}

func TestMoveCategoryIntoAnotherCategory(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "videos.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := initDB(db); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "Trips"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Trips", "beach.jpg"), []byte("image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "Archive"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Archive", "kept.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := &server{db: db}
	if _, err := srv.scanFolder(root); err != nil {
		t.Fatal(err)
	}

	body := bytes.NewBufferString(`{"from":"Trips","target":"Archive"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/categories/move", body)
	res := httptest.NewRecorder()
	srv.moveCategory(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("move status = %d body = %s", res.Code, res.Body.String())
	}

	var response struct {
		From        string `json:"from"`
		Target      string `json:"target"`
		Subcategory string `json:"subcategory"`
		Updated     int    `json:"updated"`
	}
	if err := json.NewDecoder(res.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.From != "Trips" || response.Target != "Archive" || response.Subcategory != "Trips" || response.Updated != 1 {
		t.Fatalf("unexpected move response: %+v", response)
	}

	movedFile := filepath.Join(root, "Archive", "Trips", "beach.jpg")
	if _, err := os.Stat(movedFile); err != nil {
		t.Fatalf("moved file not found: %v", err)
	}
	assertManagedDirectoryMode(t, filepath.Join(root, "Archive"))
	assertManagedDirectoryMode(t, filepath.Join(root, "Archive", "Trips"))
	if _, err := os.Stat(filepath.Join(root, "Trips", "beach.jpg")); !os.IsNotExist(err) {
		t.Fatalf("old file still exists or stat failed unexpectedly: %v", err)
	}

	var item video
	if err := db.QueryRow(
		`SELECT id, path, root, relative_path, category, subcategory, media_type, filename, title, tags, size_bytes, mtime, missing
		FROM videos WHERE title = ?`,
		"beach",
	).Scan(
		&item.ID,
		&item.Path,
		&item.Root,
		&item.RelativePath,
		&item.Category,
		&item.Subcategory,
		&item.MediaType,
		&item.Filename,
		&item.Title,
		new(string),
		&item.SizeBytes,
		&item.Mtime,
		&item.Missing,
	); err != nil {
		t.Fatal(err)
	}
	if item.Category != "Archive" || item.Subcategory != "Trips" {
		t.Fatalf("unexpected moved record: %+v", item)
	}
	if item.RelativePath != filepath.Join("Archive", "Trips", "beach.jpg") {
		t.Fatalf("unexpected moved path: %+v", item)
	}
	if got := strings.Join(categoryNames(t, db), ","); got != "Archive,Uncategorized" {
		t.Fatalf("unexpected category records after move: %s", got)
	}
}

func TestMoveCategoryRejectsNestedSource(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "videos.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := initDB(db); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "Trips"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "Trips", "Europe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Trips", "Europe", "rome.jpg"), []byte("image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "Archive"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Archive", "kept.mp4"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := &server{db: db}
	if _, err := srv.scanFolder(root); err != nil {
		t.Fatal(err)
	}

	body := bytes.NewBufferString(`{"from":"Trips","target":"Archive"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/categories/move", body)
	res := httptest.NewRecorder()
	srv.moveCategory(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("move status = %d body = %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "Only flat categories can be moved") {
		t.Fatalf("unexpected error body: %s", res.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "Trips", "Europe", "rome.jpg")); err != nil {
		t.Fatalf("source file unexpectedly changed: %v", err)
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

func TestLockedCategoriesRequireExtraPassword(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "videos.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := initDB(db); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "private"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "public.mp4"), []byte("public"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "private", "secret.mp4"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := &server{
		db:               db,
		lockedCategories: map[string]string{"private": "vault"},
	}
	if _, err := srv.scanFolder(root); err != nil {
		t.Fatal(err)
	}
	visible, err := srv.queryVideos(videoSelectSQL() + " ORDER BY id ASC")
	if err != nil {
		t.Fatal(err)
	}
	var lockedID int64
	for _, item := range visible {
		if item.Category == "private" {
			lockedID = item.ID
			break
		}
	}
	if lockedID == 0 {
		t.Fatal("locked video not found")
	}

	mux := srv.routes()

	req := httptest.NewRequest(http.MethodGet, "/api/categories", nil)
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("categories status = %d", res.Code)
	}

	var categories []categorySummary
	if err := json.NewDecoder(res.Body).Decode(&categories); err != nil {
		t.Fatal(err)
	}
	var privateCategory categorySummary
	for _, item := range categories {
		if item.Name == "private" {
			privateCategory = item
			break
		}
	}
	if !privateCategory.Locked || privateCategory.Unlocked {
		t.Fatalf("unexpected private category flags: %+v", privateCategory)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/videos", nil)
	res = httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("videos status = %d", res.Code)
	}

	var videos []video
	if err := json.NewDecoder(res.Body).Decode(&videos); err != nil {
		t.Fatal(err)
	}
	if len(videos) != 1 || videos[0].Category != "Uncategorized" {
		t.Fatalf("unexpected visible videos before unlock: %+v", videos)
	}

	req = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/media/%d", lockedID), nil)
	req.SetPathValue("id", strconv.FormatInt(lockedID, 10))
	res = httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("locked media status before unlock = %d", res.Code)
	}

	body := bytes.NewBufferString(`{"category":"private","password":"vault"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/categories/unlock", body)
	res = httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("unlock status = %d body = %s", res.Code, res.Body.String())
	}
	unlockCookies := res.Result().Cookies()
	if len(unlockCookies) == 0 {
		t.Fatal("expected unlock cookie")
	}

	req = httptest.NewRequest(http.MethodGet, "/api/videos", nil)
	for _, cookie := range unlockCookies {
		req.AddCookie(cookie)
	}
	res = httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("videos status after unlock = %d", res.Code)
	}
	if err := json.NewDecoder(res.Body).Decode(&videos); err != nil {
		t.Fatal(err)
	}
	if len(videos) != 2 {
		t.Fatalf("unexpected visible videos after unlock: %+v", videos)
	}

	body = bytes.NewBufferString(`{"category":"private"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/categories/lock", body)
	for _, cookie := range unlockCookies {
		req.AddCookie(cookie)
	}
	res = httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("lock status = %d body = %s", res.Code, res.Body.String())
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

func TestDeleteVideoTrashesFileAndPreservesRecord(t *testing.T) {
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
	if err := db.QueryRow("SELECT COUNT(*) FROM videos WHERE id = ? AND trashed_at IS NOT NULL", id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("record was not preserved in Trash")
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
	assertManagedDirectoryMode(t, filepath.Join(root, "new"))
	if _, err := os.Stat(filepath.Join(root, "old")); !os.IsNotExist(err) {
		t.Fatalf("old category folder still exists or stat failed unexpectedly: %v", err)
	}

	var item video
	err = db.QueryRow(
		"SELECT id, path, root, relative_path, category, subcategory, media_type, filename, title, tags, size_bytes, mtime, missing FROM videos WHERE filename = ?",
		"clip.mp4",
	).Scan(
		&item.ID,
		&item.Path,
		&item.Root,
		&item.RelativePath,
		&item.Category,
		&item.Subcategory,
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
	if got := strings.Join(categoryNames(t, db), ","); got != "new,Uncategorized" {
		t.Fatalf("unexpected category records after rename: %s", got)
	}
}

func TestRenameCategoryNormalizesNestedDirectoryPermissions(t *testing.T) {
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
	if err := os.Mkdir(filepath.Join(root, "old", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "old", "nested", "clip.mp4"), []byte("video"), 0o644); err != nil {
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
	if res.Code != http.StatusOK {
		t.Fatalf("rename nested status = %d body = %s", res.Code, res.Body.String())
	}

	assertManagedDirectoryMode(t, filepath.Join(root, "new"))
	assertManagedDirectoryMode(t, filepath.Join(root, "new", "nested"))
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

func TestDeleteCategoryTrashesFilesAndRetainsRecords(t *testing.T) {
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
	if err := db.QueryRow("SELECT COUNT(*) FROM videos WHERE category = ? AND trashed_at IS NULL", "doomed").Scan(&deletedCount); err != nil {
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
	if got := strings.Join(categoryNames(t, db), ","); got != "doomed,Uncategorized" {
		t.Fatalf("unexpected category records after delete: %s", got)
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

func TestDeleteVideoPreservesCategoryForRestore(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "videos.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := initDB(db); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "solo"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "solo", "clip.mp4")
	if err := os.WriteFile(path, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := &server{db: db}
	if _, err := srv.scanFolder(root); err != nil {
		t.Fatal(err)
	}

	var id int64
	if err := db.QueryRow("SELECT id FROM videos WHERE filename = ?", "clip.mp4").Scan(&id); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/videos/"+strconv.FormatInt(id, 10), nil)
	res := httptest.NewRecorder()
	srv.routes().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("delete solo category status = %d body = %s", res.Code, res.Body.String())
	}
	if got := strings.Join(categoryNames(t, db), ","); got != "solo,Uncategorized" {
		t.Fatalf("unexpected category records after deleting only item: %s", got)
	}
}

func TestMediaRequestServesContent(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "videos.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := initDB(db); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "travel"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "travel", "clip.mp4")
	if err := os.WriteFile(path, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := &server{db: db}
	if _, err := srv.scanFolder(root); err != nil {
		t.Fatal(err)
	}

	var id int64
	if err := db.QueryRow("SELECT id FROM videos WHERE filename = ?", "clip.mp4").Scan(&id); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/media/"+strconv.FormatInt(id, 10), nil)
	req.SetPathValue("id", strconv.FormatInt(id, 10))
	res := httptest.NewRecorder()
	srv.media(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("media status = %d body = %s", res.Code, res.Body.String())
	}

	if res.Body.String() != "video" {
		t.Fatalf("unexpected media content: %q", res.Body.String())
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

	videos, err := srv.queryVideos(videoSelectSQL())
	if err != nil {
		t.Fatal(err)
	}
	if len(videos) != 1 || videos[0].Root != configuredRoot || videos[0].Filename != "configured.mp4" {
		t.Fatalf("scan did not use configured root only: %+v", videos)
	}
}
