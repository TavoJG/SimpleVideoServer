package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

var videoExtensions = map[string]bool{
	".mp4": true, ".m4v": true, ".mov": true, ".webm": true, ".mkv": true,
	".avi": true, ".wmv": true, ".flv": true, ".mpeg": true, ".mpg": true,
}

var imageExtensions = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
	".bmp": true, ".tif": true, ".tiff": true, ".avif": true,
}

type server struct {
	db               *sql.DB
	defaultVideoRoot string
	password         string
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
	StreamURL    string   `json:"stream_url"`
}

type scanResponse struct {
	Root    string `json:"root"`
	Added   int    `json:"added"`
	Updated int    `json:"updated"`
	Found   int    `json:"found"`
}

type updateVideoRequest struct {
	Title    string      `json:"title"`
	Tags     interface{} `json:"tags"`
	Category *string     `json:"category"`
}

type loginRequest struct {
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
		password:         strings.TrimSpace(os.Getenv("APP_PASSWORD")),
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

func (s *server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.index)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/auth/status", s.authStatus)
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("POST /api/auth/logout", s.logout)
	mux.Handle("GET /api/config", s.authRequired(http.HandlerFunc(s.config)))
	mux.Handle("POST /api/scan", s.authRequired(http.HandlerFunc(s.scan)))
	mux.Handle("GET /api/videos", s.authRequired(http.HandlerFunc(s.listVideos)))
	mux.Handle("GET /api/videos/{id}", s.authRequired(http.HandlerFunc(s.getVideo)))
	mux.Handle("PATCH /api/videos/{id}", s.authRequired(http.HandlerFunc(s.updateVideo)))
	mux.Handle("GET /media/{id}", s.authRequired(http.HandlerFunc(s.media)))
	return mux
}

func initDB(db *sql.DB) error {
	statements := []string{
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
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_videos_missing ON videos(missing)`,
		`CREATE INDEX IF NOT EXISTS idx_videos_title ON videos(title)`,
	}

	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return err
		}
	}
	if err := ensureColumn(db, "videos", "category", "TEXT NOT NULL DEFAULT 'Uncategorized'"); err != nil {
		return err
	}
	return ensureColumn(db, "videos", "media_type", "TEXT NOT NULL DEFAULT 'video'")
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

func (s *server) index(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, filepath.Join("static", "index.html"))
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
	writeJSON(w, http.StatusOK, map[string]string{
		"default_video_root": strings.TrimSpace(s.defaultVideoRoot),
	})
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

func (s *server) listVideos(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	includeMissing := r.URL.Query().Get("include_missing") == "1"

	where := []string{}
	args := []interface{}{}
	if !includeMissing {
		where = append(where, "missing = 0")
	}
	if query != "" {
		where = append(where, "(title LIKE ? OR filename LIKE ? OR relative_path LIKE ? OR category LIKE ? OR tags LIKE ?)")
		pattern := "%" + query + "%"
		args = append(args, pattern, pattern, pattern, pattern, pattern)
	}

	sqlQuery := "SELECT id, path, root, relative_path, category, media_type, filename, title, tags, size_bytes, mtime, missing FROM videos"
	if len(where) > 0 {
		sqlQuery += " WHERE " + strings.Join(where, " AND ")
	}
	sqlQuery += " ORDER BY category COLLATE NOCASE ASC, title COLLATE NOCASE ASC"

	videos, err := s.queryVideos(sqlQuery, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load videos.")
		return
	}
	writeJSON(w, http.StatusOK, videos)
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

	if req.Category != nil {
		moved, err := moveVideoToCategory(item, *req.Category)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		item = moved
	}

	result, err := s.db.Exec(
		`UPDATE videos
		SET title = ?, tags = ?, path = ?, relative_path = ?, category = ?, filename = ?,
			updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`,
		title, tags, item.Path, item.RelativePath, item.Category, item.Filename, id,
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

	item, err = s.queryVideo(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load updated video.")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func moveVideoToCategory(item video, requestedCategory string) (video, error) {
	category, err := normalizeCategory(requestedCategory)
	if err != nil {
		return video{}, err
	}

	currentCategory := item.Category
	if currentCategory == "" {
		currentCategory = "Uncategorized"
	}
	if category == currentCategory {
		return item, nil
	}

	targetDir := item.Root
	if category != "Uncategorized" {
		targetDir = filepath.Join(item.Root, category)
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return video{}, fmt.Errorf("could not create category folder: %w", err)
	}

	targetPath := uniqueTargetPath(targetDir, item.Filename)
	if err := os.Rename(item.Path, targetPath); err != nil {
		return video{}, fmt.Errorf("could not move video: %w", err)
	}

	item.Path = targetPath
	item.Filename = filepath.Base(targetPath)
	item.Category = category
	if category == "Uncategorized" {
		item.RelativePath = item.Filename
	} else {
		item.RelativePath = filepath.Join(category, item.Filename)
	}
	item.StreamURL = fmt.Sprintf("/media/%d", item.ID)
	return item, nil
}

func normalizeCategory(value string) (string, error) {
	category := strings.TrimSpace(value)
	if category == "" || strings.EqualFold(category, "Uncategorized") {
		return "Uncategorized", nil
	}
	if category == "." || category == ".." || filepath.Base(category) != category {
		return "", fmt.Errorf("category must be a single folder name")
	}
	if strings.ContainsAny(category, `/\`) {
		return "", fmt.Errorf("category must be a single folder name")
	}
	return category, nil
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

	var path, filename string
	var missing int
	err := s.db.QueryRow("SELECT path, filename, missing FROM videos WHERE id = ?", id).Scan(&path, &filename, &missing)
	if errors.Is(err, sql.ErrNoRows) || missing != 0 {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not load video.")
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

	entries, err := os.ReadDir(root)
	if err != nil {
		return scanResponse{}, err
	}

	result := scanResponse{Root: root}
	for _, entry := range entries {
		if entry.IsDir() {
			category := entry.Name()
			childEntries, err := os.ReadDir(filepath.Join(root, category))
			if err != nil {
				return scanResponse{}, err
			}
			for _, childEntry := range childEntries {
				if childEntry.IsDir() || mediaTypeForFilename(childEntry.Name()) == "" {
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
		added, err := upsertScannedVideo(tx, root, entry.Name(), "Uncategorized", entry)
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

	if err := tx.Commit(); err != nil {
		return scanResponse{}, err
	}
	return result, nil
}

func upsertScannedVideo(tx *sql.Tx, root string, relativePath string, category string, entry os.DirEntry) (bool, error) {
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
		"SELECT id, path, root, relative_path, category, media_type, filename, title, tags, size_bytes, mtime, missing FROM videos WHERE id = ?",
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
