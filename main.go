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
	"sort"
	"strconv"
	"strings"
	"sync"
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
	categoryAliases  map[string]string
	categoryAliasMu  sync.RWMutex
	libraryMu        sync.Mutex
}

type video struct {
	ID           int64    `json:"id"`
	Path         string   `json:"path"`
	Root         string   `json:"root"`
	RelativePath string   `json:"relative_path"`
	Category     string   `json:"category"`
	MediaType    string   `json:"media_type"`
	Filename     string   `json:"filename"`
	Title        string   `json:"title"`
	Tags         []string `json:"tags"`
	SizeBytes    int64    `json:"size_bytes"`
	Mtime        float64  `json:"mtime"`
	Missing      bool     `json:"missing"`
	CreatedAt    string   `json:"created_at"`
	Favorited    bool     `json:"favorited"`
	WatchLater   bool     `json:"watch_later"`
	StreamURL    string   `json:"stream_url"`
	ThumbnailURL string   `json:"thumbnail_url"`
}

type categorySummary struct {
	Tags        []string `json:"tags"`
	Name        string   `json:"name"`
	Count       int      `json:"count"`
	DirectCount int      `json:"direct_count"`
	Locked      bool     `json:"locked"`
	Unlocked    bool     `json:"unlocked"`
}

type scanResponse struct {
	Root              string   `json:"root"`
	Added             int      `json:"added"`
	Updated           int      `json:"updated"`
	Found             int      `json:"found"`
	Missing           int      `json:"missing"`
	LastScanAt        string   `json:"last_scan_at"`
	RemovedFolders    int      `json:"removed_folders"`
	RemovedCategories int      `json:"removed_categories"`
	CleanupWarnings   []string `json:"cleanup_warnings"`
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
	Favorited   *bool       `json:"favorited"`
	WatchLater  *bool       `json:"watch_later"`
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
	if len(os.Args) > 1 && os.Args[1] == "migrate-categories" {
		if err := runCategoryMigration(os.Args[2:], os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
	dbPath := envOrDefault("VIDEO_DB", "videos.sqlite3")
	processLock, err := lockLibraryDatabase(dbPath, true)
	if err != nil {
		log.Fatal(err)
	}
	defer processLock.Close()
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
	if err := srv.loadCategoryAliases(); err != nil {
		log.Fatal(err)
	}
	if err := checkLibraryStartup(db, srv.defaultVideoRoot); err != nil {
		log.Fatal(err)
	}
	if strings.TrimSpace(srv.defaultVideoRoot) != "" {
		result, err := srv.scanFolder(srv.defaultVideoRoot)
		if err != nil {
			log.Printf("default VIDEO_ROOT scan failed: %v", err)
		} else {
			log.Printf("scanned VIDEO_ROOT %s: found=%d added=%d updated=%d removed_folders=%d removed_categories=%d", result.Root, result.Found, result.Added, result.Updated, result.RemovedFolders, result.RemovedCategories)
			for _, warning := range result.CleanupWarnings {
				log.Printf("scan cleanup warning: %s", warning)
			}
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
	mux.Handle("GET /api/category-migrations", s.authRequired(http.HandlerFunc(s.categoryMigrationMappings)))
	mux.Handle("PATCH /api/categories/tags", s.authRequired(http.HandlerFunc(s.updateCategoryTags)))
	mux.Handle("GET /api/categories", s.authRequired(http.HandlerFunc(s.listCategories)))
	mux.Handle("POST /api/categories/unlock", s.authRequired(http.HandlerFunc(s.unlockCategory)))
	mux.Handle("POST /api/categories/lock", s.authRequired(http.HandlerFunc(s.lockCategory)))
	mux.Handle("GET /api/videos", s.authRequired(http.HandlerFunc(s.listVideos)))
	mux.Handle("POST /api/videos/bulk", s.authRequired(http.HandlerFunc(s.bulkVideos)))
	mux.Handle("POST /api/categories/rename", s.authRequired(http.HandlerFunc(s.renameCategory)))
	mux.Handle("POST /api/categories/move", s.authRequired(http.HandlerFunc(s.moveCategory)))
	mux.Handle("POST /api/categories/delete", s.authRequired(http.HandlerFunc(s.deleteCategory)))
	mux.Handle("GET /media/{id}", s.authRequired(http.HandlerFunc(s.media)))
	mux.Handle("GET /thumb/{id}", s.authRequired(http.HandlerFunc(s.thumbnail)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/subcategories/") {
			writeError(w, 410, "Subcategories were removed; use flat categories.")
			return
		}
		if r.URL.Path == "/api/trash" || strings.HasPrefix(r.URL.Path, "/api/trash/") {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/videos/") && r.URL.Path != "/api/videos/bulk" {
			s.authRequired(http.HandlerFunc(s.videoItem)).ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *server) deleteCategory(w http.ResponseWriter, r *http.Request) {
	var req deleteCategoryRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "Invalid JSON body.")
		return
	}
	category, err := normalizeCategory(req.Category)
	if err != nil || category == uncategorizedCategory {
		writeError(w, 400, "Choose a managed category.")
		return
	}
	if !s.requireCategoryAccess(w, r, category) {
		return
	}
	s.libraryMu.Lock()
	defer s.libraryMu.Unlock()
	result, err := s.deleteFolder(category, "")
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, result)
}

func initDB(db *sql.DB) error { return initDatabase(db, false) }

func initDatabase(db *sql.DB, migrating bool) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS category_tag_seeds(root TEXT NOT NULL,parent TEXT NOT NULL,subcategory TEXT NOT NULL,PRIMARY KEY(root,parent,subcategory))`,
		`CREATE TABLE IF NOT EXISTS schema_versions (name TEXT PRIMARY KEY)`,
		`CREATE TABLE IF NOT EXISTS category_migrations (root TEXT NOT NULL,parent TEXT NOT NULL,subcategory TEXT NOT NULL,target TEXT NOT NULL,done INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(root,parent,subcategory))`,
		`CREATE TABLE IF NOT EXISTS category_lock_aliases (category TEXT PRIMARY KEY COLLATE NOCASE,parent TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS library_roots (root TEXT PRIMARY KEY)`,
		`CREATE TABLE IF NOT EXISTS pending_restorations (id INTEGER PRIMARY KEY,source TEXT NOT NULL,target TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS pending_deletions (id INTEGER PRIMARY KEY,stage TEXT NOT NULL,record TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS videos (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			path TEXT NOT NULL UNIQUE,
			root TEXT NOT NULL,
			relative_path TEXT NOT NULL,
			category TEXT NOT NULL DEFAULT 'Uncategorized',
			media_type TEXT NOT NULL DEFAULT 'video',
			filename TEXT NOT NULL,
			title TEXT NOT NULL,
			tags TEXT NOT NULL DEFAULT '',
			size_bytes INTEGER NOT NULL DEFAULT 0,
			mtime REAL NOT NULL DEFAULT 0,
			missing INTEGER NOT NULL DEFAULT 0,
			favorited INTEGER NOT NULL DEFAULT 0,
			watch_later INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS categories (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL COLLATE NOCASE UNIQUE,
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
	if err := ensureColumn(db, "categories", "tags", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return err
	}
	if migrating {
		if err := ensureColumn(db, "videos", "subcategory", "TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	if err := ensureColumn(db, "videos", "media_type", "TEXT NOT NULL DEFAULT 'video'"); err != nil {
		return err
	}
	if err := ensureColumn(db, "videos", "favorited", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := ensureColumn(db, "videos", "watch_later", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := ensureColumn(db, "videos", "created_at", "TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP"); err != nil {
		return err
	}
	for table, columns := range map[string][]string{
		"videos":     {"playback_position_seconds", "duration_seconds", "last_played_at"},
		"categories": {"last_reproduced"},
	} {
		for _, column := range columns {
			var exists int
			if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?", table, column).Scan(&exists); err != nil {
				return err
			}
			if exists != 0 {
				if _, err := db.Exec("ALTER TABLE " + table + " DROP COLUMN " + column); err != nil {
					return err
				}
			}
		}
	}
	if err := recoverPendingDeletions(db); err != nil {
		return err
	}
	if err := migrateLegacyTrash(db); err != nil {
		return err
	}
	if err := migrateTagsJSON(db); err != nil {
		return err
	}
	if !migrating {
		if err := finishFlatSchema(db); err != nil {
			return err
		}
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
	folders, err := discoverLibraryFolders(tx)
	if err != nil {
		return err
	}
	names := map[string]bool{uncategorizedCategory: true}
	for _, folder := range folders {
		names[folder.category] = true
	}
	for name := range names {
		if err := ensureCategoryTx(tx, name); err != nil {
			return err
		}
	}
	rows, err := tx.Query("SELECT name FROM categories")
	if err != nil {
		return err
	}
	var obsolete []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		if !names[name] {
			obsolete = append(obsolete, name)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, name := range obsolete {
		if _, err := tx.Exec("DELETE FROM categories WHERE name=?", name); err != nil {
			return err
		}
	}
	return seedCategoryTagsTx(tx)
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
	return syncCategoriesTx(tx)
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
	s.libraryMu.Lock()
	defer s.libraryMu.Unlock()
	folders, err := discoverLibraryFolders(s.db)
	if err != nil {
		return nil, err
	}
	summaries := map[string]*categorySummary{uncategorizedCategory: {Name: uncategorizedCategory, Tags: []string{}}}
	for _, folder := range folders {
		item := summaries[folder.category]
		if item == nil {
			item = &categorySummary{Name: folder.category, Tags: []string{}}
			summaries[folder.category] = item
		}
	}
	tagRows, err := s.db.Query("SELECT name,tags FROM categories")
	if err != nil {
		return nil, err
	}
	for tagRows.Next() {
		var name, tags string
		if err := tagRows.Scan(&name, &tags); err != nil {
			tagRows.Close()
			return nil, err
		}
		if item := summaries[name]; item != nil {
			item.Tags = splitTags(tags)
		}
	}
	err = tagRows.Err()
	tagRows.Close()
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query("SELECT category,COUNT(*) FROM videos WHERE missing=0 GROUP BY category")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var cat string
		var count int
		if err := rows.Scan(&cat, &count); err != nil {
			return nil, err
		}
		item := summaries[cat]
		if item == nil {
			continue
		}
		item.Count += count
		item.DirectCount += count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := []categorySummary{}
	for _, item := range summaries {
		result = append(result, *item)
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name) })
	return result, nil
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
		if !categories[index].Unlocked {
			categories[index].Tags = []string{}
		}
	}
	writeJSON(w, http.StatusOK, categories)
}

func (s *server) listVideos(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	includeMissing := r.URL.Query().Get("include_missing") == "1"

	where := []string{"1=1"}
	args := []interface{}{}
	if !includeMissing {
		where = append(where, "missing = 0")
	}
	if query != "" {
		where = append(where, "(title LIKE ? OR filename LIKE ? OR relative_path LIKE ? OR category LIKE ? OR tags LIKE ?)")
		pattern := "%" + query + "%"
		args = append(args, pattern, pattern, pattern, pattern, pattern)
	}

	sqlQuery := videoSelectSQL()
	if len(where) > 0 {
		sqlQuery += " WHERE " + strings.Join(where, " AND ")
	}
	sqlQuery += " ORDER BY category COLLATE NOCASE ASC, title COLLATE NOCASE ASC"

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
		writeError(w, 400, "Invalid JSON body.")
		return
	}
	if req.Subcategory != nil {
		writeError(w, 400, "Subcategories were removed; omit subcategory.")
		return
	}
	s.libraryMu.Lock()
	defer s.libraryMu.Unlock()
	item, err := s.queryVideo(id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, 500, "Could not load media.")
		return
	}
	if !s.requireCategoryAccess(w, r, item.Category) {
		return
	}
	if req.Category != nil && !s.requireCategoryAccess(w, r, *req.Category) {
		return
	}
	updated, err := s.updateMedia(item, req)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, updated)
}

func (s *server) deleteVideo(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	s.libraryMu.Lock()
	defer s.libraryMu.Unlock()
	item, err := s.queryVideo(id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, 500, "Could not load media.")
		return
	}
	if !s.requireCategoryAccess(w, r, item.Category) {
		return
	}
	err = s.permanentDelete(item)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": true})
}

func (s *server) renameCategory(w http.ResponseWriter, r *http.Request) {
	s.managedRenameCategory(w, r)
}

func (s *server) moveCategory(w http.ResponseWriter, r *http.Request) { s.managedMoveCategory(w, r) }

func moveVideoToLocation(item video, requestedCategory string) (video, error) {
	category, err := normalizeCategory(requestedCategory)
	if err != nil {
		return video{}, err
	}
	current := item.Category
	if current == "" {
		current = uncategorizedCategory
	}
	if category == current {
		return item, nil
	}
	targetDir := locationDirectory(item.Root, category, "")
	if err := checkFolderPath(item.Root, targetDir); err != nil {
		return video{}, err
	}
	if category != uncategorizedCategory {
		if err := ensureManagedDirectory(targetDir); err != nil {
			return video{}, err
		}
	}
	target, err := uniqueTargetPath(targetDir, item.Filename)
	if err != nil {
		return video{}, err
	}
	if err := os.Rename(item.Path, target); err != nil {
		return video{}, fmt.Errorf("could not move media: %w", err)
	}
	item.Path = target
	item.Filename = filepath.Base(target)
	item.Category = category
	item.RelativePath = buildRelativePath(category, "", item.Filename)
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
	if strings.EqualFold(value, legacyTrashDirectory) || strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("%s is reserved or invalid", label)
	}
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

func (s *server) isCategoryLocked(category string) bool { return s.categoryPassword(category) != "" }
func (s *server) categoryPassword(category string) string {
	key := categoryKey(category)
	if password := s.lockedCategories[key]; password != "" {
		return password
	}
	s.categoryAliasMu.RLock()
	parent, ok := s.categoryAliases[key]
	s.categoryAliasMu.RUnlock()
	if !ok {
		return ""
	}
	return s.lockedCategories[categoryKey(parent)]
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

func uniqueTargetPath(dir string, filename string) (string, error) {
	ext := filepath.Ext(filename)
	stem := strings.TrimSuffix(filename, ext)
	candidate := filepath.Join(dir, filename)
	for index := 1; ; index++ {
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", fmt.Errorf("Could not check destination: %w", err)
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
	s.libraryMu.Lock()
	defer s.libraryMu.Unlock()
	root, err := filepath.Abs(expandHome(rootValue))
	if err != nil {
		return scanResponse{}, err
	}

	info, err := os.Lstat(root)
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

	result := scanResponse{Root: root, CleanupWarnings: []string{}}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || entry.Name() == legacyTrashDirectory {
			continue
		}
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
				if childEntry.Type()&os.ModeSymlink != 0 {
					continue
				}
				if childEntry.IsDir() {
					result.CleanupWarnings = append(result.CleanupWarnings, "Nested folder is not indexed: "+filepath.Join(root, category, childEntry.Name()))
					continue
				}
				if mediaTypeForFilename(childEntry.Name()) == "" {
					continue
				}
				added, err := upsertScannedVideo(tx, root, filepath.Join(category, childEntry.Name()), category, childEntry)
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
		added, err := upsertScannedVideo(tx, root, entry.Name(), uncategorizedCategory, entry)
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
	if _, err := tx.Exec("INSERT OR IGNORE INTO library_roots(root) VALUES(?)", root); err != nil {
		return scanResponse{}, err
	}
	if err := tx.QueryRow("SELECT COUNT(*) FROM videos WHERE root = ? AND missing = 1", root).Scan(&result.Missing); err != nil {
		return scanResponse{}, err
	}

	if err := tx.Commit(); err != nil {
		return scanResponse{}, err
	}
	oldNames, err := categoryNameSet(s.db)
	if err != nil {
		return scanResponse{}, err
	}
	removed, warnings := cleanupEmptyDirectories(root)
	result.RemovedFolders = removed
	result.CleanupWarnings = append(result.CleanupWarnings, warnings...)
	if err := syncCategories(s.db); err != nil {
		return scanResponse{}, err
	}
	newNames, err := categoryNameSet(s.db)
	if err != nil {
		return scanResponse{}, err
	}
	for name := range oldNames {
		if !newNames[name] {
			result.RemovedCategories++
		}
	}
	result.LastScanAt = time.Now().UTC().Format(time.RFC3339)
	return result, nil
}

func upsertScannedVideo(tx *sql.Tx, root string, relativePath string, category string, entry os.DirEntry) (bool, error) {
	if err := ensureCategoryTx(tx, category); err != nil {
		return false, err
	}

	absolutePath := filepath.Join(root, relativePath)
	mediaType := mediaTypeForFilename(relativePath)
	if mediaType == "" {
		return false, fmt.Errorf("unsupported media file: %s", relativePath)
	}
	info, err := entry.Info()
	if err == nil && !info.Mode().IsRegular() {
		return false, fmt.Errorf("media must be a regular file: %s", absolutePath)
	}
	if err != nil {
		return false, err
	}

	var id int64
	err = tx.QueryRow("SELECT id FROM videos WHERE path = ?", absolutePath).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.Exec(
			`INSERT INTO videos (
				path, root, relative_path, category, media_type, filename, title, size_bytes, mtime
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			absolutePath,
			root,
			relativePath,
			category,
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
				mtime = ?, category = ?, media_type = ?, missing = 0, updated_at = CURRENT_TIMESTAMP
			WHERE id = ?`,
		root,
		relativePath,
		filepath.Base(absolutePath),
		info.Size(),
		float64(info.ModTime().UnixNano())/1e9,
		category,
		mediaType,
		id,
	)
	return false, err
}

func (s *server) queryVideo(id int64) (video, error) {
	videos, err := s.queryVideos(
		videoSelectSQL()+" WHERE id = ?",
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

func videoSelectSQL() string {
	return `SELECT id, path, root, relative_path, category, media_type, filename, title, tags,
		size_bytes, mtime, missing, created_at,
		favorited, watch_later FROM videos`
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
		var favorited int
		var watchLater int
		if err := rows.Scan(
			&item.ID,
			&item.Path,
			&item.Root,
			&item.RelativePath,
			&item.Category,
			&item.MediaType,
			&item.Filename,
			&item.Title,
			&tags,
			&item.SizeBytes,
			&item.Mtime,
			&missing,
			&item.CreatedAt,
			&favorited,
			&watchLater,
		); err != nil {
			return nil, err
		}
		item.Tags = splitTags(tags)
		item.Missing = missing != 0
		item.Favorited = favorited != 0
		item.WatchLater = watchLater != 0
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
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
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
	raw := []string{}
	switch tags := value.(type) {
	case []interface{}:
		for _, tag := range tags {
			raw = append(raw, fmt.Sprint(tag))
		}
	case []string:
		raw = tags
	case string:
		raw = splitTags(tags)
	}
	seen := map[string]bool{}
	clean := []string{}
	for _, tag := range raw {
		tag = strings.TrimSpace(tag)
		key := strings.ToLower(tag)
		if tag != "" && !seen[key] {
			seen[key] = true
			clean = append(clean, tag)
		}
	}
	data, _ := json.Marshal(clean)
	return string(data)
}
func splitTags(value string) []string {
	var tags []string
	if json.Unmarshal([]byte(value), &tags) == nil && tags != nil {
		return tags
	}
	tags = []string{}
	for _, tag := range strings.Split(value, ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}

func boolValueOrDefault(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
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
