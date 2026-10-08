package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type classificationResult struct {
	Subcategory string `json:"subcategory"`
	IsNew       bool   `json:"is_new"`
	Abstain     bool   `json:"abstain"`
	Reason      string `json:"reason"`
}

func (s *server) initClassification() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS classification_jobs (id INTEGER PRIMARY KEY, category TEXT NOT NULL, status TEXT NOT NULL, total INTEGER NOT NULL, processed INTEGER NOT NULL DEFAULT 0, created_at TEXT DEFAULT CURRENT_TIMESTAMP);
 CREATE TABLE IF NOT EXISTS classification_suggestions (id INTEGER PRIMARY KEY, job_id INTEGER NOT NULL, video_id INTEGER NOT NULL, path TEXT NOT NULL, mtime REAL NOT NULL, size INTEGER NOT NULL, subcategory TEXT NOT NULL DEFAULT '', reason TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'pending', error TEXT NOT NULL DEFAULT '');
 CREATE UNIQUE INDEX IF NOT EXISTS classification_one_running ON classification_jobs(category) WHERE status='running';
 UPDATE classification_jobs SET status='interrupted' WHERE status='running';`)
	return err
}

func (s *server) startClassification(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("CLASSIFIER_URL") == "" {
		writeError(w, 503, "Classifier is not configured.")
		return
	}
	var req struct {
		Category string `json:"category"`
	}
	if readJSON(r, &req) != nil {
		writeError(w, 400, "Invalid JSON.")
		return
	}
	category, err := normalizeCategory(req.Category)
	if err != nil || category == uncategorizedCategory {
		writeError(w, 400, "Select a parent category.")
		return
	}
	if !s.requireCategoryAccess(w, r, category) {
		return
	}
	rows, err := s.db.Query(`SELECT id FROM videos WHERE category=? AND subcategory='' AND missing=0 AND trashed_at IS NULL`, category)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	var items []video
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	for _, id := range ids {
		item, e := s.queryVideo(id)
		if e != nil {
			writeError(w, 500, e.Error())
			return
		}
		items = append(items, item)
	}
	result, err := s.db.Exec(`INSERT INTO classification_jobs(category,status,total) VALUES(?,'running',?)`, category, len(items))
	if err != nil {
		writeError(w, 409, "A classification job is already running or could not be started.")
		return
	}
	id, _ := result.LastInsertId()
	ctx, cancel := context.WithCancel(context.Background())
	s.classificationMu.Lock()
	if s.classificationCancels == nil {
		s.classificationCancels = map[int64]context.CancelFunc{}
	}
	s.classificationCancels[id] = cancel
	s.classificationMu.Unlock()
	go s.runClassification(ctx, id, category, items)
	writeJSON(w, 202, map[string]any{"id": id})
}

func frameTimes(duration float64) []float64 {
	count := 8
	if duration < 8 {
		count = int(duration)
		if count < 1 {
			count = 1
		}
	}
	times := make([]float64, count)
	for i := range times {
		times[i] = duration * float64(i+1) / float64(count+1)
	}
	return times
}

func visualPreviews(ctx context.Context, item video) ([]string, error) {
	times := []float64{0}
	if item.MediaType == "video" {
		out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", item.Path).Output()
		if err != nil {
			return nil, fmt.Errorf("probe video: %w", err)
		}
		duration, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
		if err != nil || math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 || duration > 1e8 {
			return nil, fmt.Errorf("invalid video duration")
		}
		times = frameTimes(duration)
	}
	previews := []string{}
	for _, at := range times {
		args := []string{"-v", "error"}
		if item.MediaType == "video" {
			args = append(args, "-ss", fmt.Sprintf("%.4f", at))
		}
		args = append(args, "-i", item.Path, "-frames:v", "1", "-vf", "scale=768:768:force_original_aspect_ratio=decrease", "-f", "image2pipe", "-vcodec", "mjpeg", "pipe:1")
		out, err := exec.CommandContext(ctx, "ffmpeg", args...).Output()
		if err != nil {
			return nil, fmt.Errorf("extract preview: %w", err)
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("empty preview")
		}
		previews = append(previews, base64.StdEncoding.EncodeToString(out))
	}
	return previews, nil
}

func (s *server) classify(ctx context.Context, item video, category string) (classificationResult, error) {
	var result classificationResult
	timeout := 180
	if n, e := strconv.Atoi(os.Getenv("CLASSIFIER_TIMEOUT_SECONDS")); e == nil && n > 0 {
		timeout = n
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	images, err := visualPreviews(ctx, item)
	if err != nil {
		return result, err
	}
	names := []string{}
	rows, err := s.db.Query(`SELECT DISTINCT subcategory FROM videos WHERE category=? AND subcategory<>'' AND trashed_at IS NULL`, category)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			break
		}
		names = append(names, name)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return result, err
	}
	body, _ := json.Marshal(map[string]any{"parent_category": category, "existing_subcategories": names, "images": images})
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(os.Getenv("CLASSIFIER_URL"), "/")+"/classify", bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+os.Getenv("CLASSIFIER_TOKEN"))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return result, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return result, fmt.Errorf("classifier returned HTTP %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return result, fmt.Errorf("invalid classifier response size")
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		return result, err
	}
	for _, key := range []string{"subcategory", "is_new", "abstain", "reason"} {
		if value, ok := fields[key]; !ok || string(value) == "null" {
			return result, fmt.Errorf("missing classifier field %s", key)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&result); err != nil {
		return result, err
	}
	if result.Abstain {
		result.Subcategory = ""
		return result, nil
	}
	name, err := normalizeSubcategory(result.Subcategory)
	if err != nil || name == "" || len(name) > 100 || strings.ContainsRune(name, 0) {
		return result, fmt.Errorf("invalid classifier destination")
	}
	result.Subcategory = name
	result.IsNew = true
	for _, existing := range names {
		if strings.EqualFold(existing, name) {
			result.Subcategory = existing
			result.IsNew = false
			break
		}
	}
	return result, nil
}

func (s *server) runClassification(ctx context.Context, id int64, category string, items []video) {
	defer func() { s.classificationMu.Lock(); delete(s.classificationCancels, id); s.classificationMu.Unlock() }()
	status := "completed"
	for _, item := range items {
		if ctx.Err() != nil {
			status = "cancelled"
			break
		}
		current, loadErr := s.queryVideo(item.ID)
		if loadErr != nil || current.TrashedAt != "" {
			_, _ = s.db.Exec(`UPDATE classification_jobs SET processed=processed+1 WHERE id=?`, id)
			continue
		}
		result, err := s.classify(ctx, item, category)
		if ctx.Err() != nil {
			status = "cancelled"
			break
		}
		state, message := "pending", ""
		if err != nil {
			state = "error"
			message = err.Error()
		}
		if err == nil && result.Abstain {
			state = "abstained"
		}
		_, dbErr := s.db.Exec(`INSERT INTO classification_suggestions(job_id,video_id,path,mtime,size,subcategory,reason,status,error) VALUES(?,?,?,?,?,?,?,?,?)`, id, item.ID, item.Path, item.Mtime, item.SizeBytes, result.Subcategory, result.Reason, state, message)
		if dbErr != nil {
			status = "failed"
			break
		}
		if _, err = s.db.Exec(`UPDATE classification_jobs SET processed=processed+1 WHERE id=?`, id); err != nil {
			status = "failed"
			break
		}
	}
	s.db.Exec(`UPDATE classification_jobs SET status=? WHERE id=?`, status, id)
}

func (s *server) listClassification(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")
	if !s.requireCategoryAccess(w, r, category) {
		return
	}
	jobs := []map[string]any{}
	rows, err := s.db.Query(`SELECT id,status,total,processed FROM classification_jobs WHERE category=? ORDER BY id DESC`, category)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	for rows.Next() {
		var id int64
		var status string
		var total, processed int
		if err = rows.Scan(&id, &status, &total, &processed); err != nil {
			break
		}
		jobs = append(jobs, map[string]any{"id": id, "status": status, "total": total, "processed": processed})
	}
	rows.Close()
	suggestions := []map[string]any{}
	rows, err = s.db.Query(`SELECT a.id,a.video_id,a.subcategory,a.reason,a.status,a.error FROM classification_suggestions a JOIN classification_jobs j ON j.id=a.job_id WHERE j.category=? AND j.id=(SELECT MAX(id) FROM classification_jobs WHERE category=?) ORDER BY a.id`, category, category)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	for rows.Next() {
		var id, vid int64
		var sub, reason, status, message string
		if err = rows.Scan(&id, &vid, &sub, &reason, &status, &message); err != nil {
			break
		}
		item, e := s.queryVideo(vid)
		if e != nil || item.TrashedAt != "" || !s.canAccessCategory(r, item.Category) {
			continue
		}
		suggestions = append(suggestions, map[string]any{"id": id, "video_id": vid, "subcategory": sub, "reason": reason, "status": status, "error": message, "title": item.Title, "thumbnail_url": item.ThumbnailURL})
	}
	rows.Close()
	writeJSON(w, 200, map[string]any{"enabled": os.Getenv("CLASSIFIER_URL") != "", "jobs": jobs, "suggestions": suggestions})
}

func (s *server) cancelClassification(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID int64 `json:"id"`
	}
	if readJSON(r, &req) != nil {
		writeError(w, 400, "Invalid JSON.")
		return
	}
	var category string
	if s.db.QueryRow(`SELECT category FROM classification_jobs WHERE id=?`, req.ID).Scan(&category) != nil {
		writeError(w, 404, "Job not found.")
		return
	}
	if !s.requireCategoryAccess(w, r, category) {
		return
	}
	s.classificationMu.Lock()
	if cancel := s.classificationCancels[req.ID]; cancel != nil {
		cancel()
	}
	s.classificationMu.Unlock()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *server) applyClassification(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Items []struct {
			ID          int64  `json:"id"`
			Subcategory string `json:"subcategory"`
		} `json:"items"`
	}
	if readJSON(r, &req) != nil {
		writeError(w, 400, "Invalid JSON.")
		return
	}
	results := []map[string]any{}
	for _, choice := range req.Items {
		err := s.applySuggestion(r, choice.ID, choice.Subcategory)
		message := ""
		if err != nil {
			message = err.Error()
		}
		results = append(results, map[string]any{"id": choice.ID, "error": message})
	}
	writeJSON(w, 200, map[string]any{"results": results})
}

func (s *server) applySuggestion(r *http.Request, id int64, sub string) error {
	s.libraryMu.Lock()
	defer s.libraryMu.Unlock()
	s.classificationMu.Lock()
	defer s.classificationMu.Unlock()
	var vid, size int64
	var path, category, status string
	var mtime float64
	if err := s.db.QueryRow(`SELECT a.video_id,a.path,a.mtime,a.size,j.category,a.status FROM classification_suggestions a JOIN classification_jobs j ON j.id=a.job_id WHERE a.id=?`, id).Scan(&vid, &path, &mtime, &size, &category, &status); err != nil {
		return err
	}
	if !s.canAccessCategory(r, category) {
		return fmt.Errorf("category is locked")
	}
	if status != "pending" {
		return fmt.Errorf("suggestion is not pending")
	}
	item, err := s.queryVideo(vid)
	if err != nil {
		return err
	}
	if item.TrashedAt != "" {
		return fmt.Errorf("media is in Trash")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if item.Path != path || item.Category != category || item.Subcategory != "" || item.Mtime != mtime || info.Size() != size || float64(info.ModTime().UnixNano())/1e9 != mtime {
		return fmt.Errorf("media changed; classify it again")
	}
	normalized, err := normalizeSubcategory(sub)
	if err != nil || normalized == "" || len(normalized) > 100 || strings.ContainsRune(normalized, 0) {
		return fmt.Errorf("choose a valid subcategory")
	}
	_, err = s.updateMedia(item, updateVideoRequest{Title: item.Title, Tags: item.Tags, Category: &category, Subcategory: &normalized})
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE classification_suggestions SET status='applied',subcategory=?,error='' WHERE id=?`, normalized, id)
	return err
}
