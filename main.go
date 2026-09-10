package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed frontend/dist
var frontendDist embed.FS

var videoExtensions = map[string]bool{
	".mp4": true, ".m4v": true, ".mov": true, ".webm": true, ".mkv": true,
	".avi": true, ".wmv": true, ".flv": true, ".mpeg": true, ".mpg": true,
}

var imageExtensions = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
	".bmp": true, ".tif": true, ".tiff": true, ".avif": true,
}

const uncategorizedCategory = "Uncategorized"
const managedDirectoryMode = 0o2775

type server struct {
	db               *sql.DB
	defaultVideoRoot string
	thumbnailDir     string
	password         string
	lockedCategories map[string]string
}

type video struct {
	ID           int64    `json:"id"`
	Path         string   `json:"path"`
	Root         string   `json:"root"`
	RelativePath string   `json:"relative_path"`
	Category     string   `json:"category"`
	Subcategory  string   `json:"subcategory"`
	MediaType    string   `json:"media_type"`
	Filename     string   `json:"filename"`
	Title        string   `json:"title"`
	Tags         []string `json:"tags"`
	SizeBytes    int64    `json:"size_bytes"`
	Mtime        float64  `json:"mtime"`
	Missing      bool     `json:"missing"`
	StreamURL    string   `json:"stream_url"`
	ThumbnailURL string   `json:"thumbnail_url"`
}

type categorySummary struct {
	Name           string               `json:"name"`
	Count          int                  `json:"count"`
	DirectCount    int                  `json:"direct_count"`
	LastReproduced *string              `json:"last_reproduced"`
	Locked         bool                 `json:"locked"`
	Unlocked       bool                 `json:"unlocked"`
	Subcategories  []subcategorySummary `json:"subcategories"`
}

type subcategorySummary struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type scanResponse struct {
	Root    string `json:"root"`
	Added   int    `json:"added"`
	Updated int    `json:"updated"`
	Found   int    `json:"found"`
}

type storageUsage struct {
	TotalBytes     uint64 `json:"total_bytes"`
	UsedBytes      uint64 `json:"used_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
}

type configResponse struct {
	DefaultVideoRoot string        `json:"default_video_root"`
	Storage          *storageUsage `json:"storage,omitempty"`
}

type updateVideoRequest struct {
	Title       string      `json:"title"`
	Tags        interface{} `json:"tags"`
	Category    *string     `json:"category"`
	Subcategory *string     `json:"subcategory"`
}

type renameCategoryRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type moveCategoryRequest struct {
	From   string `json:"from"`
	Target string `json:"target"`
}

type deleteCategoryRequest struct {
	Category string `json:"category"`
}

type loginRequest struct {
	Password string `json:"password"`
}

type categoryUnlockRequest struct {
	Category string `json:"category"`
	Password string `json:"password"`
}

func main() {
	dbPath := envOrDefault("VIDEO_DB", "videos.sqlite3")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if err := initDB(db); err != nil {
		log.Fatal(err)
	}

	srv := &server{
		db:               db,
		defaultVideoRoot: os.Getenv("VIDEO_ROOT"),
		thumbnailDir:     envOrDefault("VIDEO_THUMB_DIR", "thumbnails"),
		password:         strings.TrimSpace(os.Getenv("APP_PASSWORD")),
		lockedCategories: parseLockedCategories(os.Getenv("APP_LOCKED_CATEGORIES")),
	}
	if strings.TrimSpace(srv.defaultVideoRoot) != "" {
		result, err := srv.scanFolder(srv.defaultVideoRoot)
		if err != nil {
			log.Printf("default VIDEO_ROOT scan failed: %v", err)
		} else {
			log.Printf("scanned VIDEO_ROOT %s: found=%d added=%d updated=%d", result.Root, result.Found, result.Added, result.Updated)
		}
	}

	mux := srv.routes()

	addr := ":" + envOrDefault("PORT", "5000")
	log.Printf("listening on http://127.0.0.1%s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.index)
	frontendFiles, err := fs.Sub(frontendDist, "frontend/dist")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /assets/", http.FileServer(http.FS(frontendFiles)))
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/auth/status", s.authStatus)
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("POST /api/auth/logout", s.logout)
	mux.Handle("GET /api/config", s.authRequired(http.HandlerFunc(s.config)))
	mux.Handle("POST /api/scan", s.authRequired(http.HandlerFunc(s.scan)))
	mux.Handle("GET /api/categories", s.authRequired(http.HandlerFunc(s.listCategories)))
	mux.Handle("POST /api/categories/unlock", s.authRequired(http.HandlerFunc(s.unlockCategory)))
	mux.Handle("POST /api/categories/lock", s.authRequired(http.HandlerFunc(s.lockCategory)))
	mux.Handle("GET /api/videos", s.authRequired(http.HandlerFunc(s.listVideos)))
	mux.Handle("POST /api/categories/rename", s.authRequired(http.HandlerFunc(s.renameCategory)))
	mux.Handle("POST /api/categories/move", s.authRequired(http.HandlerFunc(s.moveCategory)))
	mux.Handle("POST /api/categories/delete", s.authRequired(http.HandlerFunc(s.deleteCategory)))
	mux.Handle("GET /media/{id}", s.authRequired(http.HandlerFunc(s.media)))
	mux.Handle("GET /thumb/{id}", s.authRequired(http.HandlerFunc(s.thumbnail)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/videos/") {
			s.authRequired(http.HandlerFunc(s.videoItem)).ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *server) deleteCategory(w http.ResponseWriter, r *http.Request) {
	var req deleteCategoryRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON body.")
		return
	}

	category, err := normalizeCategory(req.Category)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if category == uncategorizedCategory {
		writeError(w, http.StatusBadRequest, "Uncategorized cannot be deleted.")
		return
	}
	if !s.requireCategoryAccess(w, r, category) {
		return
	}

	rows, err := s.db.Query("SELECT id, path, root FROM videos WHERE category = ?", category)
	if err != nil {
		log.Printf("delete category query failed: category=%q err=%v", category, err)
		writeError(w, http.StatusInternalServerError, "Could not load category.")
		return
	}
	defer rows.Close()

	type categoryItem struct {
		id   int64
		path string
		root string
	}
	items := []categoryItem{}
	roots := map[string]bool{}
	for rows.Next() {
		var item categoryItem
		if err := rows.Scan(&item.id, &item.path, &item.root); err != nil {
			log.Printf("delete category scan failed: category=%q err=%v", category, err)
			writeError(w, http.StatusInternalServerError, "Could not load category.")
			return
		}
		items = append(items, item)
		roots[item.root] = true
	}
	if err := rows.Err(); err != nil {
		log.Printf("delete category rows iteration failed: category=%q err=%v", category, err)
		writeError(w, http.StatusInternalServerError, "Could not load category.")
		return
	}
	if len(items) == 0 {
		http.NotFound(w, r)
		return
	}

	for _, item := range items {
		if err := os.Remove(item.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("delete category file removal failed: category=%q video_id=%d path=%q err=%v", category, item.id, item.path, err)
			writeError(w, http.StatusInternalServerError, "Could not delete category media files.")
			return
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("delete category begin tx failed: category=%q err=%v", category, err)
		writeError(w, http.StatusInternalServerError, "Could not delete category records.")
		return
	}
	defer tx.Rollback()

	result, err := tx.Exec("DELETE FROM videos WHERE category = ?", category)
	if err != nil {
		log.Printf("delete category videos delete failed: category=%q err=%v", category, err)
		writeError(w, http.StatusInternalServerError, "Could not delete category records.")
		return
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		log.Printf("delete category rows affected failed: category=%q err=%v", category, err)
		writeError(w, http.StatusInternalServerError, "Could not delete category records.")
		return
	}
	if _, err := tx.Exec("DELETE FROM categories WHERE name = ?", category); err != nil {
		log.Printf("delete category category row delete failed: category=%q err=%v", category, err)
		writeError(w, http.StatusInternalServerError, "Could not delete category records.")
		return
	}
	if err := tx.Commit(); err != nil {
		log.Printf("delete category commit failed: category=%q deleted=%d err=%v", category, deleted, err)
		writeError(w, http.StatusInternalServerError, "Could not delete category records.")
		return
	}

	for root := range roots {
		categoryDir := filepath.Join(root, category)
		if err := os.RemoveAll(categoryDir); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("delete category could not remove folder %s: %v", categoryDir, err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"category": category,
		"deleted":  deleted,
	})
}

func initDB(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS videos (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			path TEXT NOT NULL UNIQUE,
			root TEXT NOT NULL,
			relative_path TEXT NOT NULL,
			category TEXT NOT NULL DEFAULT 'Uncategorized',
			subcategory TEXT NOT NULL DEFAULT '',
			media_type TEXT NOT NULL DEFAULT 'video',
			filename TEXT NOT NULL,
			title TEXT NOT NULL,
			tags TEXT NOT NULL DEFAULT '',
			size_bytes INTEGER NOT NULL DEFAULT 0,
			mtime REAL NOT NULL DEFAULT 0,
			missing INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS categories (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL COLLATE NOCASE UNIQUE,
			last_reproduced TEXT,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_videos_missing ON videos(missing)`,
		`CREATE INDEX IF NOT EXISTS idx_videos_title ON videos(title)`,
		`CREATE INDEX IF NOT EXISTS idx_categories_name ON categories(name COLLATE NOCASE)`,
	}

	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return err
		}
	}
	if err := ensureColumn(db, "videos", "category", "TEXT NOT NULL DEFAULT 'Uncategorized'"); err != nil {
		return err
	}
	if err := ensureColumn(db, "videos", "subcategory", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureColumn(db, "videos", "media_type", "TEXT NOT NULL DEFAULT 'video'"); err != nil {
		return err
	}
	if err := ensureColumn(db, "categories", "last_reproduced", "TEXT"); err != nil {
		return err
	}
	return syncCategories(db)
}

func ensureColumn(db *sql.DB, table string, column string, definition string) error {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == column {
			return rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	_, err = db.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + definition)
	return err
}

func syncCategories(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := syncCategoriesTx(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func syncCategoriesTx(tx *sql.Tx) error {
	if err := ensureCategoryTx(tx, uncategorizedCategory); err != nil {
		return err
	}

	rows, err := tx.Query("SELECT DISTINCT category FROM videos")
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var category string
		if err := rows.Scan(&category); err != nil {
			return err
		}
		normalized, err := normalizeCategory(category)
		if err != nil {
			return err
		}
		if err := ensureCategoryTx(tx, normalized); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if _, err := tx.Exec(
		`DELETE FROM categories
		WHERE name <> ? AND NOT EXISTS (
			SELECT 1 FROM videos WHERE videos.category = categories.name
		)`,
		uncategorizedCategory,
	); err != nil {
		return err
	}
	return nil
}

func ensureCategoryTx(tx *sql.Tx, category string) error {
	normalized, err := normalizeCategory(category)
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		`INSERT INTO categories (name)
		VALUES (?)
		ON CONFLICT(name) DO NOTHING`,
		normalized,
	)
	return err
}

func deleteCategoryIfUnusedTx(tx *sql.Tx, category string) error {
	normalized, err := normalizeCategory(category)
	if err != nil {
		return err
	}
	if normalized == uncategorizedCategory {
		return nil
	}
	_, err = tx.Exec(
		`DELETE FROM categories
		WHERE name = ? AND NOT EXISTS (
			SELECT 1 FROM videos WHERE videos.category = categories.name
		)`,
		normalized,
	)
	return err
}

func renameCategoryRecordTx(tx *sql.Tx, from string, to string) error {
	result, err := tx.Exec(
		`UPDATE categories
		SET name = ?, updated_at = CURRENT_TIMESTAMP
		WHERE name = ?`,
		to, from,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ensureCategoryTx(tx, to)
	}
	return nil
}

func (s *server) queryCategories() ([]categorySummary, error) {
	rows, err := s.db.Query(
		`SELECT
			c.name,
			COUNT(v.id) AS item_count,
			COUNT(CASE WHEN v.subcategory = '' THEN 1 END) AS direct_count,
			c.last_reproduced
		FROM categories c
		LEFT JOIN videos v
			ON v.category = c.name AND v.missing = 0
		GROUP BY c.id, c.name, c.last_reproduced
		ORDER BY c.name COLLATE NOCASE ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var categories []categorySummary
	for rows.Next() {
		var item categorySummary
		var lastReproduced sql.NullString
		if err := rows.Scan(&item.Name, &item.Count, &item.DirectCount, &lastReproduced); err != nil {
			return nil, err
		}
		if lastReproduced.Valid {
			value := lastReproduced.String
			item.LastReproduced = &value
		}
		item.Subcategories = []subcategorySummary{}
		categories = append(categories, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	subcategoryRows, err := s.db.Query(
		`SELECT category, subcategory, COUNT(id) AS item_count
		FROM videos
		WHERE missing = 0 AND subcategory <> ''
		GROUP BY category, subcategory
		ORDER BY category COLLATE NOCASE ASC, subcategory COLLATE NOCASE ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer subcategoryRows.Close()

	indexByCategory := map[string]int{}
	for index, item := range categories {
		indexByCategory[item.Name] = index
	}
	for subcategoryRows.Next() {
		var categoryName string
		var item subcategorySummary
		if err := subcategoryRows.Scan(&categoryName, &item.Name, &item.Count); err != nil {
			return nil, err
		}
		index, ok := indexByCategory[categoryName]
		if !ok {
			continue
		}
		categories[index].Subcategories = append(categories[index].Subcategories, item)
	}
	if err := subcategoryRows.Err(); err != nil {
		return nil, err
	}
	return categories, nil
}

func (s *server) touchCategoryLastReproduced(category string) {
	normalized, err := normalizeCategory(category)
	if err != nil {
		return
	}
	if _, err := s.db.Exec(
		`UPDATE categories
		SET last_reproduced = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE name = ?`,
		normalized,
	); err != nil {
		log.Printf("touch category last_reproduced failed for %s: %v", normalized, err)
	}
}

func (s *server) index(w http.ResponseWriter, r *http.Request) {
	index, err := frontendDist.ReadFile("frontend/dist/index.html")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load frontend.")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(index)
}

func (s *server) authRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.isAuthenticated(r) {
			next.ServeHTTP(w, r)
			return
		}
		writeError(w, http.StatusUnauthorized, "Authentication required.")
	})
}

func (s *server) authStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{
		"enabled":       s.authEnabled(),
		"authenticated": s.isAuthenticated(r),
	})
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled() {
		writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
		return
	}

	var req loginRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON body.")
		return
	}
	if !constantStringEqual(req.Password, s.password) {
		writeError(w, http.StatusUnauthorized, "Invalid password.")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "video_server_auth",
		Value:    s.authToken(),
		Path:     "/",
		MaxAge:   60 * 60 * 24 * 30,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "video_server_auth",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	for category := range s.lockedCategories {
		http.SetCookie(w, &http.Cookie{
			Name:     s.categoryCookieName(category),
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
	}
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}

func (s *server) authEnabled() bool {
	return s.password != ""
}

func (s *server) isAuthenticated(r *http.Request) bool {
	if !s.authEnabled() {
		return true
	}
	cookie, err := r.Cookie("video_server_auth")
	if err != nil {
		return false
	}
	return constantStringEqual(cookie.Value, s.authToken())
}

func (s *server) authToken() string {
	mac := hmac.New(sha256.New, []byte(s.password))
	mac.Write([]byte("video-server-auth"))
	return hex.EncodeToString(mac.Sum(nil))
}

func constantStringEqual(a string, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) config(w http.ResponseWriter, r *http.Request) {
	root := strings.TrimSpace(s.defaultVideoRoot)
	response := configResponse{DefaultVideoRoot: root}
	if root != "" {
		usage, err := storageForPath(root)
		if err != nil {
			log.Printf("storage usage unavailable for %q: %v", root, err)
		} else {
			response.Storage = &usage
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func storageForPath(path string) (storageUsage, error) {
	var stats syscall.Statfs_t
	if err := syscall.Statfs(path, &stats); err != nil {
		return storageUsage{}, err
	}

	blockSize := uint64(stats.Bsize)
	totalBytes := stats.Blocks * blockSize
	usedBytes := (stats.Blocks - stats.Bfree) * blockSize
	availableBytes := stats.Bavail * blockSize
	return storageUsage{
		TotalBytes:     totalBytes,
		UsedBytes:      usedBytes,
		AvailableBytes: availableBytes,
	}, nil
}

func (s *server) scan(w http.ResponseWriter, r *http.Request) {
	root := strings.TrimSpace(s.defaultVideoRoot)
	if root == "" {
		writeError(w, http.StatusBadRequest, "VIDEO_ROOT is not configured.")
		return
	}

	result, err := s.scanFolder(root)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *server) listCategories(w http.ResponseWriter, r *http.Request) {
	categories, err := s.queryCategories()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load categories.")
		return
	}
	for index := range categories {
		categories[index].Locked = s.isCategoryLocked(categories[index].Name)
		categories[index].Unlocked = s.canAccessCategory(r, categories[index].Name)
	}
	writeJSON(w, http.StatusOK, categories)
}

func (s *server) listVideos(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	includeMissing := r.URL.Query().Get("include_missing") == "1"

	where := []string{}
	args := []interface{}{}
	if !includeMissing {
		where = append(where, "missing = 0")
	}
	if query != "" {
		where = append(where, "(title LIKE ? OR filename LIKE ? OR relative_path LIKE ? OR category LIKE ? OR subcategory LIKE ? OR tags LIKE ?)")
		pattern := "%" + query + "%"
		args = append(args, pattern, pattern, pattern, pattern, pattern, pattern)
	}

	sqlQuery := "SELECT id, path, root, relative_path, category, subcategory, media_type, filename, title, tags, size_bytes, mtime, missing FROM videos"
	if len(where) > 0 {
		sqlQuery += " WHERE " + strings.Join(where, " AND ")
	}
	sqlQuery += " ORDER BY category COLLATE NOCASE ASC, subcategory COLLATE NOCASE ASC, title COLLATE NOCASE ASC"

	videos, err := s.queryVideos(sqlQuery, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load videos.")
		return
	}
	filtered := videos[:0]
	for _, item := range videos {
		if s.canAccessCategory(r, item.Category) {
			filtered = append(filtered, item)
		}
	}
	writeJSON(w, http.StatusOK, filtered)
}

func (s *server) videoItem(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/videos/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" || len(parts) > 2 || (len(parts) == 2 && parts[1] != "delete") {
		http.NotFound(w, r)
		return
	}
	r.SetPathValue("id", parts[0])

	if len(parts) == 2 {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			writeError(w, http.StatusMethodNotAllowed, "Method not allowed.")
			return
		}
		s.deleteVideo(w, r)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.getVideo(w, r)
	case http.MethodPatch:
		s.updateVideo(w, r)
	case http.MethodDelete:
		s.deleteVideo(w, r)
	default:
		w.Header().Set("Allow", "GET, PATCH, DELETE")
		writeError(w, http.StatusMethodNotAllowed, "Method not allowed.")
	}
}

func (s *server) getVideo(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}

	item, err := s.queryVideo(id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load video.")
		return
	}
	if !s.requireCategoryAccess(w, r, item.Category) {
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *server) updateVideo(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}

	var req updateVideoRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON body.")
		return
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		writeError(w, http.StatusBadRequest, "Title is required.")
		return
	}

	tags := normalizeTags(req.Tags)
	item, err := s.queryVideo(id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load video.")
		return
	}
	previousCategory := item.Category
	if !s.requireCategoryAccess(w, r, previousCategory) {
		return
	}

	if req.Category != nil {
		if !s.requireCategoryAccess(w, r, *req.Category) {
			return
		}
		requestedSubcategory := item.Subcategory
		if req.Subcategory != nil {
			requestedSubcategory = *req.Subcategory
		}
		moved, err := moveVideoToLocation(item, *req.Category, requestedSubcategory)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		item = moved
	} else if req.Subcategory != nil {
		moved, err := moveVideoToLocation(item, item.Category, *req.Subcategory)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		item = moved
	}

	oldCategory, err := normalizeCategory(previousCategory)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not update video.")
		return
	}

	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not update video.")
		return
	}
	defer tx.Rollback()

	if err := ensureCategoryTx(tx, item.Category); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not update video.")
		return
	}
	result, err := tx.Exec(
		`UPDATE videos
		SET title = ?, tags = ?, path = ?, relative_path = ?, category = ?, subcategory = ?, filename = ?,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		title, tags, item.Path, item.RelativePath, item.Category, item.Subcategory, item.Filename, id,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not update video.")
		return
	}
	affected, err := result.RowsAffected()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not update video.")
		return
	}
	if affected == 0 {
		http.NotFound(w, r)
		return
	}
	if err := deleteCategoryIfUnusedTx(tx, oldCategory); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not update video.")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not update video.")
		return
	}

	item, err = s.queryVideo(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load updated video.")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *server) deleteVideo(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}

	item, err := s.queryVideo(id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("delete video query failed: video_id=%d err=%v", id, err)
		writeError(w, http.StatusInternalServerError, "Could not load media.")
		return
	}
	if !s.requireCategoryAccess(w, r, item.Category) {
		return
	}

	if err := os.Remove(item.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("delete video file removal failed: video_id=%d category=%q path=%q err=%v", id, item.Category, item.Path, err)
		writeError(w, http.StatusInternalServerError, "Could not delete media file.")
		return
	}

	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("delete video begin tx failed: video_id=%d category=%q err=%v", id, item.Category, err)
		writeError(w, http.StatusInternalServerError, "Could not delete media record.")
		return
	}
	defer tx.Rollback()

	result, err := tx.Exec("DELETE FROM videos WHERE id = ?", id)
	if err != nil {
		log.Printf("delete video row delete failed: video_id=%d category=%q err=%v", id, item.Category, err)
		writeError(w, http.StatusInternalServerError, "Could not delete media record.")
		return
	}
	affected, err := result.RowsAffected()
	if err != nil {
		log.Printf("delete video rows affected failed: video_id=%d category=%q err=%v", id, item.Category, err)
		writeError(w, http.StatusInternalServerError, "Could not delete media record.")
		return
	}
	if affected == 0 {
		http.NotFound(w, r)
		return
	}
	if err := deleteCategoryIfUnusedTx(tx, item.Category); err != nil {
		log.Printf("delete video cleanup category failed: video_id=%d category=%q err=%v", id, item.Category, err)
		writeError(w, http.StatusInternalServerError, "Could not delete media record.")
		return
	}
	if err := tx.Commit(); err != nil {
		log.Printf("delete video commit failed: video_id=%d category=%q err=%v", id, item.Category, err)
		writeError(w, http.StatusInternalServerError, "Could not delete media record.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *server) renameCategory(w http.ResponseWriter, r *http.Request) {
	var req renameCategoryRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON body.")
		return
	}

	from, err := normalizeCategory(req.From)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	to, err := normalizeCategory(req.To)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if from == uncategorizedCategory || to == uncategorizedCategory {
		writeError(w, http.StatusBadRequest, "Uncategorized cannot be renamed.")
		return
	}
	if s.isCategoryLocked(from) || s.isCategoryLocked(to) {
		writeError(w, http.StatusBadRequest, "Locked categories cannot be renamed.")
		return
	}
	if strings.EqualFold(from, to) {
		writeError(w, http.StatusBadRequest, "Choose a different category name.")
		return
	}

	var targetCount int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM categories WHERE name = ? COLLATE NOCASE", to).Scan(&targetCount); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not check target category.")
		return
	}
	if targetCount > 0 {
		writeError(w, http.StatusBadRequest, "Target category already exists.")
		return
	}

	rows, err := s.db.Query("SELECT id, root, filename, subcategory FROM videos WHERE category = ?", from)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load category.")
		return
	}
	defer rows.Close()

	type categoryItem struct {
		id          int64
		root        string
		filename    string
		subcategory string
	}
	items := []categoryItem{}
	roots := map[string]bool{}
	for rows.Next() {
		var item categoryItem
		if err := rows.Scan(&item.id, &item.root, &item.filename, &item.subcategory); err != nil {
			writeError(w, http.StatusInternalServerError, "Could not load category.")
			return
		}
		items = append(items, item)
		roots[item.root] = true
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load category.")
		return
	}
	if len(items) == 0 {
		http.NotFound(w, r)
		return
	}

	for root := range roots {
		targetDir := filepath.Join(root, to)
		if _, err := os.Stat(targetDir); err == nil {
			writeError(w, http.StatusBadRequest, "Target category already exists.")
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusInternalServerError, "Could not check target category.")
			return
		}
	}

	renamedRoots := []string{}
	for root := range roots {
		fromDir := filepath.Join(root, from)
		toDir := filepath.Join(root, to)
		if _, err := os.Stat(fromDir); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "Could not check category folder.")
			return
		}
		if err := os.Rename(fromDir, toDir); err != nil {
			for i := len(renamedRoots) - 1; i >= 0; i-- {
				previousRoot := renamedRoots[i]
				if rollbackErr := os.Rename(filepath.Join(previousRoot, to), filepath.Join(previousRoot, from)); rollbackErr != nil {
					log.Printf("rename category rollback failed: %v", rollbackErr)
				}
			}
			writeError(w, http.StatusInternalServerError, "Could not rename category folder.")
			return
		}
		if err := normalizeManagedDirectoryTree(toDir); err != nil {
			log.Printf("rename category permission normalization failed: from=%q to=%q root=%q err=%v", from, to, root, err)
		}
		renamedRoots = append(renamedRoots, root)
	}

	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not update category.")
		return
	}
	defer tx.Rollback()

	for _, item := range items {
		newRelativePath := buildRelativePath(to, item.subcategory, item.filename)
		newPath := filepath.Join(item.root, newRelativePath)
		if _, err := tx.Exec(
			`UPDATE videos
			SET path = ?, relative_path = ?, category = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ?`,
			newPath, newRelativePath, to, item.id,
		); err != nil {
			writeError(w, http.StatusInternalServerError, "Could not update category.")
			return
		}
	}
	if err := renameCategoryRecordTx(tx, from, to); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not update category.")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not update category.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"from":    from,
		"to":      to,
		"updated": len(items),
	})
}

func (s *server) moveCategory(w http.ResponseWriter, r *http.Request) {
	var req moveCategoryRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON body.")
		return
	}

	from, err := normalizeCategory(req.From)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	target, err := normalizeCategory(req.Target)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if from == uncategorizedCategory {
		writeError(w, http.StatusBadRequest, "Uncategorized cannot be moved.")
		return
	}
	if target == uncategorizedCategory {
		writeError(w, http.StatusBadRequest, "Choose a destination category.")
		return
	}
	if strings.EqualFold(from, target) {
		writeError(w, http.StatusBadRequest, "Choose a different destination category.")
		return
	}
	if s.isCategoryLocked(from) || s.isCategoryLocked(target) {
		writeError(w, http.StatusBadRequest, "Locked categories cannot be moved.")
		return
	}
	if !s.requireCategoryAccess(w, r, from) || !s.requireCategoryAccess(w, r, target) {
		return
	}

	var targetCount int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM categories WHERE name = ? COLLATE NOCASE", target).Scan(&targetCount); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not check target category.")
		return
	}
	if targetCount == 0 {
		writeError(w, http.StatusBadRequest, "Destination category does not exist.")
		return
	}

	var subcategoryConflictCount int
	if err := s.db.QueryRow(
		"SELECT COUNT(*) FROM videos WHERE category = ? AND subcategory = ?",
		target,
		from,
	).Scan(&subcategoryConflictCount); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not check destination category.")
		return
	}
	if subcategoryConflictCount > 0 {
		writeError(w, http.StatusBadRequest, "Destination category already has that subcategory.")
		return
	}

	rows, err := s.db.Query(
		`SELECT id, path, root, relative_path, category, subcategory, media_type, filename, title, tags, size_bytes, mtime, missing
		FROM videos
		WHERE category = ?`,
		from,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load category.")
		return
	}
	defer rows.Close()

	items := []video{}
	roots := map[string]bool{}
	for rows.Next() {
		var item video
		var rawTags string
		if err := rows.Scan(
			&item.ID,
			&item.Path,
			&item.Root,
			&item.RelativePath,
			&item.Category,
			&item.Subcategory,
			&item.MediaType,
			&item.Filename,
			&item.Title,
			&rawTags,
			&item.SizeBytes,
			&item.Mtime,
			&item.Missing,
		); err != nil {
			writeError(w, http.StatusInternalServerError, "Could not load category.")
			return
		}
		item.Tags = splitTags(rawTags)
		if item.Subcategory != "" {
			writeError(w, http.StatusBadRequest, "Only flat categories can be moved into another category.")
			return
		}
		if item.Missing {
			writeError(w, http.StatusBadRequest, "Category contains missing files and cannot be moved.")
			return
		}
		items = append(items, item)
		roots[item.Root] = true
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load category.")
		return
	}
	if len(items) == 0 {
		http.NotFound(w, r)
		return
	}

	for root := range roots {
		targetDir := locationDirectory(root, target, from)
		if _, err := os.Stat(targetDir); err == nil {
			writeError(w, http.StatusBadRequest, "Destination category already has that subcategory.")
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			writeError(w, http.StatusInternalServerError, "Could not check destination category.")
			return
		}
	}

	movedItems := make([]video, 0, len(items))
	for _, item := range items {
		moved, err := moveVideoToLocation(item, target, from)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		movedItems = append(movedItems, moved)
	}

	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not update category.")
		return
	}
	defer tx.Rollback()

	if err := ensureCategoryTx(tx, target); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not update category.")
		return
	}
	for _, item := range movedItems {
		if _, err := tx.Exec(
			`UPDATE videos
			SET path = ?, relative_path = ?, category = ?, subcategory = ?, filename = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ?`,
			item.Path,
			item.RelativePath,
			item.Category,
			item.Subcategory,
			item.Filename,
			item.ID,
		); err != nil {
			writeError(w, http.StatusInternalServerError, "Could not update category.")
			return
		}
	}
	if err := deleteCategoryIfUnusedTx(tx, from); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not update category.")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not update category.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"from":        from,
		"target":      target,
		"subcategory": from,
		"updated":     len(movedItems),
	})
}

func moveVideoToLocation(item video, requestedCategory string, requestedSubcategory string) (video, error) {
	category, subcategory, err := normalizeCategorySelection(requestedCategory, requestedSubcategory)
	if err != nil {
		return video{}, err
	}

	currentCategory := item.Category
	if currentCategory == "" {
		currentCategory = uncategorizedCategory
	}
	currentSubcategory := item.Subcategory
	if category == currentCategory && subcategory == currentSubcategory {
		return item, nil
	}

	targetDir := locationDirectory(item.Root, category, subcategory)
	if category != uncategorizedCategory {
		if err := ensureManagedDirectory(locationDirectory(item.Root, category, "")); err != nil {
			return video{}, fmt.Errorf("could not create category folder: %w", err)
		}
	}
	if subcategory != "" {
		if err := ensureManagedDirectory(targetDir); err != nil {
			return video{}, fmt.Errorf("could not create category folder: %w", err)
		}
	} else if category == uncategorizedCategory {
		if err := os.MkdirAll(targetDir, managedDirectoryMode); err != nil {
			return video{}, fmt.Errorf("could not create category folder: %w", err)
		}
	}

	targetPath := uniqueTargetPath(targetDir, item.Filename)
	if err := os.Rename(item.Path, targetPath); err != nil {
		return video{}, fmt.Errorf("could not move video: %w", err)
	}

	item.Path = targetPath
	item.Filename = filepath.Base(targetPath)
	item.Category = category
	item.Subcategory = subcategory
	item.RelativePath = buildRelativePath(category, subcategory, item.Filename)
	item.StreamURL = fmt.Sprintf("/media/%d", item.ID)
	return item, nil
}

func ensureManagedDirectory(path string) error {
	if err := os.MkdirAll(path, managedDirectoryMode); err != nil {
		return fmt.Errorf("mkdir %s: %w", path, err)
	}
	if err := os.Chmod(path, managedDirectoryMode); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

func normalizeManagedDirectoryTree(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if err := os.Chmod(path, managedDirectoryMode); err != nil {
			return fmt.Errorf("chmod %s: %w", path, err)
		}
		return nil
	})
}

func normalizeCategory(value string) (string, error) {
	category := strings.TrimSpace(value)
	if category == "" || strings.EqualFold(category, uncategorizedCategory) {
		return uncategorizedCategory, nil
	}
	return normalizeFolderName(category, "category")
}

func normalizeSubcategory(value string) (string, error) {
	subcategory := strings.TrimSpace(value)
	if subcategory == "" {
		return "", nil
	}
	return normalizeFolderName(subcategory, "subcategory")
}

func normalizeFolderName(value string, label string) (string, error) {
	if value == "." || value == ".." || filepath.Base(value) != value {
		return "", fmt.Errorf("%s must be a single folder name", label)
	}
	if strings.ContainsAny(value, `/\`) {
		return "", fmt.Errorf("%s must be a single folder name", label)
	}
	return value, nil
}

func normalizeCategorySelection(categoryValue string, subcategoryValue string) (string, string, error) {
	category, err := normalizeCategory(categoryValue)
	if err != nil {
		return "", "", err
	}
	subcategory, err := normalizeSubcategory(subcategoryValue)
	if err != nil {
		return "", "", err
	}
	if category == uncategorizedCategory {
		subcategory = ""
	}
	return category, subcategory, nil
}

func buildRelativePath(category string, subcategory string, filename string) string {
	switch {
	case category == uncategorizedCategory:
		return filename
	case subcategory == "":
		return filepath.Join(category, filename)
	default:
		return filepath.Join(category, subcategory, filename)
	}
}

func locationDirectory(root string, category string, subcategory string) string {
	if category == uncategorizedCategory {
		return root
	}
	if subcategory == "" {
		return filepath.Join(root, category)
	}
	return filepath.Join(root, category, subcategory)
}

func categoryKey(value string) string {
	category, err := normalizeCategory(value)
	if err != nil {
		return ""
	}
	return strings.ToLower(category)
}

func parseLockedCategories(raw string) map[string]string {
	locked := map[string]string{}
	for _, entry := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ';' || r == '\n' || r == '\r'
	}) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		name, password, ok := strings.Cut(entry, "=")
		if !ok {
			log.Printf("ignoring invalid APP_LOCKED_CATEGORIES entry %q", entry)
			continue
		}
		category, err := normalizeCategory(name)
		if err != nil {
			log.Printf("ignoring invalid locked category %q: %v", name, err)
			continue
		}
		password = strings.TrimSpace(password)
		if password == "" {
			log.Printf("ignoring locked category %q with empty password", category)
			continue
		}
		locked[strings.ToLower(category)] = password
	}
	return locked
}

func (s *server) isCategoryLocked(category string) bool {
	if len(s.lockedCategories) == 0 {
		return false
	}
	_, ok := s.lockedCategories[categoryKey(category)]
	return ok
}

func (s *server) categoryPassword(category string) string {
	return s.lockedCategories[categoryKey(category)]
}

func (s *server) categoryCookieName(category string) string {
	key := categoryKey(category)
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return "video_server_lock_" + hex.EncodeToString(sum[:8])
}

func (s *server) categoryToken(category string) string {
	password := s.categoryPassword(category)
	if password == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(password))
	mac.Write([]byte("video-server-category-lock:"))
	mac.Write([]byte(categoryKey(category)))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *server) canAccessCategory(r *http.Request, category string) bool {
	if !s.isCategoryLocked(category) {
		return true
	}
	cookie, err := r.Cookie(s.categoryCookieName(category))
	if err != nil {
		return false
	}
	return constantStringEqual(cookie.Value, s.categoryToken(category))
}

func (s *server) requireCategoryAccess(w http.ResponseWriter, r *http.Request, category string) bool {
	if s.canAccessCategory(r, category) {
		return true
	}
	writeError(w, http.StatusForbidden, "Locked category password required.")
	return false
}

func (s *server) unlockCategory(w http.ResponseWriter, r *http.Request) {
	var req categoryUnlockRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON body.")
		return
	}
	category, err := normalizeCategory(req.Category)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	password := s.categoryPassword(category)
	if password == "" {
		writeError(w, http.StatusBadRequest, "Category is not locked.")
		return
	}
	if !constantStringEqual(req.Password, password) {
		writeError(w, http.StatusUnauthorized, "Invalid category password.")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.categoryCookieName(category),
		Value:    s.categoryToken(category),
		Path:     "/",
		MaxAge:   60 * 60 * 12,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"category": category,
		"unlocked": true,
	})
}

func (s *server) lockCategory(w http.ResponseWriter, r *http.Request) {
	var req categoryUnlockRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON body.")
		return
	}
	category, err := normalizeCategory(req.Category)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.isCategoryLocked(category) {
		writeError(w, http.StatusBadRequest, "Category is not locked.")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.categoryCookieName(category),
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"category": category,
		"unlocked": false,
	})
}

func uniqueTargetPath(dir string, filename string) string {
	ext := filepath.Ext(filename)
	stem := strings.TrimSuffix(filename, ext)
	candidate := filepath.Join(dir, filename)
	for index := 1; ; index++ {
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
		candidate = filepath.Join(dir, fmt.Sprintf("%s_%d%s", stem, index, ext))
	}
}

func mediaTypeForFilename(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	if videoExtensions[ext] {
		return "video"
	}
	if imageExtensions[ext] {
		return "image"
	}
	return ""
}

func (s *server) media(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}

	var path, filename, category string
	var missing int
	err := s.db.QueryRow("SELECT path, filename, category, missing FROM videos WHERE id = ?", id).Scan(&path, &filename, &category, &missing)
	if errors.Is(err, sql.ErrNoRows) || missing != 0 {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load video.")
		return
	}
	if !s.requireCategoryAccess(w, r, category) {
		return
	}

	file, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil || stat.IsDir() {
		http.NotFound(w, r)
		return
	}

	s.touchCategoryLastReproduced(category)
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", filename))
	http.ServeContent(w, r, filename, stat.ModTime(), file)
}

func (s *server) thumbnail(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}

	var path, filename, mediaType, category string
	var mtime float64
	var missing int
	err := s.db.QueryRow(
		"SELECT path, filename, media_type, category, mtime, missing FROM videos WHERE id = ?",
		id,
	).Scan(&path, &filename, &mediaType, &category, &mtime, &missing)
	if errors.Is(err, sql.ErrNoRows) || missing != 0 {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load thumbnail.")
		return
	}
	if !s.requireCategoryAccess(w, r, category) {
		return
	}
	if mediaType == "image" {
		s.media(w, r)
		return
	}
	if mediaType != "video" {
		http.NotFound(w, r)
		return
	}

	thumbnailPath, err := s.ensureVideoThumbnail(id, path, mtime)
	if err != nil {
		log.Printf("thumbnail generation failed for %s: %v", filename, err)
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, thumbnailPath)
}

func (s *server) ensureVideoThumbnail(id int64, mediaPath string, mtime float64) (string, error) {
	thumbnailDir := strings.TrimSpace(s.thumbnailDir)
	if thumbnailDir == "" {
		thumbnailDir = "thumbnails"
	}
	if err := os.MkdirAll(thumbnailDir, 0o755); err != nil {
		return "", err
	}

	thumbnailPath := filepath.Join(thumbnailDir, fmt.Sprintf("%d-%.0f.jpg", id, mtime))
	if info, err := os.Stat(thumbnailPath); err == nil && !info.IsDir() {
		return thumbnailPath, nil
	}

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return "", fmt.Errorf("ffmpeg is not installed")
	}

	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-y",
		"-ss", "1",
		"-i", mediaPath,
		"-frames:v", "1",
		"-vf", "scale=320:-1",
		thumbnailPath,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if output, err := exec.CommandContext(ctx, "ffmpeg", args...).CombinedOutput(); err != nil {
		_ = os.Remove(thumbnailPath)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("ffmpeg timed out")
		}
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return thumbnailPath, nil
}

func (s *server) scanFolder(rootValue string) (scanResponse, error) {
	root, err := filepath.Abs(expandHome(rootValue))
	if err != nil {
		return scanResponse{}, err
	}

	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return scanResponse{}, fmt.Errorf("folder does not exist: %s", root)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return scanResponse{}, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("UPDATE videos SET missing = 1, updated_at = CURRENT_TIMESTAMP WHERE root = ?", root); err != nil {
		return scanResponse{}, err
	}
	if err := ensureCategoryTx(tx, uncategorizedCategory); err != nil {
		return scanResponse{}, err
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return scanResponse{}, err
	}

	result := scanResponse{Root: root}
	for _, entry := range entries {
		if entry.IsDir() {
			category, err := normalizeCategory(entry.Name())
			if err != nil {
				continue
			}
			childEntries, err := os.ReadDir(filepath.Join(root, category))
			if err != nil {
				return scanResponse{}, err
			}
			for _, childEntry := range childEntries {
				if childEntry.IsDir() {
					subcategory, err := normalizeSubcategory(childEntry.Name())
					if err != nil {
						continue
					}
					grandchildEntries, err := os.ReadDir(filepath.Join(root, category, subcategory))
					if err != nil {
						return scanResponse{}, err
					}
					for _, grandchildEntry := range grandchildEntries {
						if grandchildEntry.IsDir() || mediaTypeForFilename(grandchildEntry.Name()) == "" {
							continue
						}
						added, err := upsertScannedVideo(
							tx,
							root,
							filepath.Join(category, subcategory, grandchildEntry.Name()),
							category,
							subcategory,
							grandchildEntry,
						)
						if err != nil {
							return scanResponse{}, err
						}
						result.Found++
						if added {
							result.Added++
						} else {
							result.Updated++
						}
					}
					continue
				}
				if mediaTypeForFilename(childEntry.Name()) == "" {
					continue
				}
				added, err := upsertScannedVideo(tx, root, filepath.Join(category, childEntry.Name()), category, "", childEntry)
				if err != nil {
					return scanResponse{}, err
				}
				result.Found++
				if added {
					result.Added++
				} else {
					result.Updated++
				}
			}
			continue
		}
		if mediaTypeForFilename(entry.Name()) == "" {
			continue
		}
		added, err := upsertScannedVideo(tx, root, entry.Name(), uncategorizedCategory, "", entry)
		if err != nil {
			return scanResponse{}, err
		}
		result.Found++
		if added {
			result.Added++
		} else {
			result.Updated++
		}
	}
	if err := syncCategoriesTx(tx); err != nil {
		return scanResponse{}, err
	}

	if err := tx.Commit(); err != nil {
		return scanResponse{}, err
	}
	return result, nil
}

func upsertScannedVideo(tx *sql.Tx, root string, relativePath string, category string, subcategory string, entry os.DirEntry) (bool, error) {
	if err := ensureCategoryTx(tx, category); err != nil {
		return false, err
	}

	absolutePath := filepath.Join(root, relativePath)
	mediaType := mediaTypeForFilename(relativePath)
	if mediaType == "" {
		return false, fmt.Errorf("unsupported media file: %s", relativePath)
	}
	info, err := entry.Info()
	if err != nil {
		return false, err
	}

	var id int64
	err = tx.QueryRow("SELECT id FROM videos WHERE path = ?", absolutePath).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.Exec(
			`INSERT INTO videos (
				path, root, relative_path, category, subcategory, media_type, filename, title, size_bytes, mtime
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			absolutePath,
			root,
			relativePath,
			category,
			subcategory,
			mediaType,
			filepath.Base(absolutePath),
			strings.TrimSuffix(filepath.Base(absolutePath), filepath.Ext(absolutePath)),
			info.Size(),
			float64(info.ModTime().UnixNano())/1e9,
		)
		return true, err
	}
	if err != nil {
		return false, err
	}

	_, err = tx.Exec(
		`UPDATE videos
			SET root = ?, relative_path = ?, filename = ?, size_bytes = ?,
				mtime = ?, category = ?, subcategory = ?, media_type = ?, missing = 0, updated_at = CURRENT_TIMESTAMP
			WHERE id = ?`,
		root,
		relativePath,
		filepath.Base(absolutePath),
		info.Size(),
		float64(info.ModTime().UnixNano())/1e9,
		category,
		subcategory,
		mediaType,
		id,
	)
	return false, err
}

func (s *server) queryVideo(id int64) (video, error) {
	videos, err := s.queryVideos(
		"SELECT id, path, root, relative_path, category, subcategory, media_type, filename, title, tags, size_bytes, mtime, missing FROM videos WHERE id = ?",
		id,
	)
	if err != nil {
		return video{}, err
	}
	if len(videos) == 0 {
		return video{}, sql.ErrNoRows
	}
	return videos[0], nil
}

func (s *server) queryVideos(query string, args ...interface{}) ([]video, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	videos := []video{}
	for rows.Next() {
		var item video
		var tags string
		var missing int
		if err := rows.Scan(
			&item.ID,
			&item.Path,
			&item.Root,
			&item.RelativePath,
			&item.Category,
			&item.Subcategory,
			&item.MediaType,
			&item.Filename,
			&item.Title,
			&tags,
			&item.SizeBytes,
			&item.Mtime,
			&missing,
		); err != nil {
			return nil, err
		}
		item.Tags = splitTags(tags)
		item.Missing = missing != 0
		item.StreamURL = fmt.Sprintf("/media/%d", item.ID)
		if item.MediaType == "image" {
			item.ThumbnailURL = item.StreamURL
		} else {
			item.ThumbnailURL = fmt.Sprintf("/thumb/%d", item.ID)
		}
		videos = append(videos, item)
	}
	return videos, rows.Err()
}

func readJSON(r *http.Request, target interface{}) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(target)
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write json: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func parseID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "Invalid video id.")
		return 0, false
	}
	return id, true
}

func normalizeTags(value interface{}) string {
	var raw []string
	switch tags := value.(type) {
	case []interface{}:
		for _, tag := range tags {
			raw = append(raw, fmt.Sprint(tag))
		}
	case []string:
		raw = tags
	case string:
		raw = strings.Split(tags, ",")
	}

	seen := map[string]bool{}
	clean := []string{}
	for _, tag := range raw {
		tag = strings.TrimSpace(tag)
		key := strings.ToLower(tag)
		if tag != "" && !seen[key] {
			clean = append(clean, tag)
			seen[key] = true
		}
	}
	return strings.Join(clean, ", ")
}

func splitTags(value string) []string {
	if strings.TrimSpace(value) == "" {
		return []string{}
	}

	parts := strings.Split(value, ",")
	tags := []string{}
	for _, part := range parts {
		if tag := strings.TrimSpace(part); tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}

func expandHome(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}

func envOrDefault(key string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
