package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func classificationFixture(t *testing.T) (*server, video) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = initDB(db); err != nil {
		t.Fatal(err)
	}
	s := &server{db: db}
	if err = s.initClassification(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "Nature")
	os.Mkdir(dir, 0755)
	path := filepath.Join(dir, "image.jpg")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	jpeg.Encode(file, image.NewRGBA(image.Rect(0, 0, 1000, 600)), nil)
	file.Close()
	if _, err = s.scanFolder(root); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err = db.QueryRow(`SELECT id FROM videos`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	item, err := s.queryVideo(id)
	if err != nil {
		t.Fatal(err)
	}
	return s, item
}

func addSuggestion(t *testing.T, s *server, item video) int64 {
	t.Helper()
	job, err := s.db.Exec(`INSERT INTO classification_jobs(category,status,total) VALUES(?,'completed',1)`, item.Category)
	if err != nil {
		t.Fatal(err)
	}
	jid, _ := job.LastInsertId()
	res, err := s.db.Exec(`INSERT INTO classification_suggestions(job_id,video_id,path,mtime,size,subcategory) VALUES(?,?,?,?,?,'Forests')`, jid, item.ID, item.Path, item.Mtime, item.SizeBytes)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestClassificationApplyAndStale(t *testing.T) {
	s, item := classificationFixture(t)
	id := addSuggestion(t, s, item)
	req := httptest.NewRequest("POST", "/", nil)
	if err := s.applySuggestion(req, id, "Forests"); err != nil {
		t.Fatal(err)
	}
	moved, err := s.queryVideo(item.ID)
	if err != nil || moved.Subcategory != "Forests" {
		t.Fatalf("move: %+v %v", moved, err)
	}
	if _, err = os.Stat(moved.Path); err != nil {
		t.Fatal(err)
	}
	if err = s.applySuggestion(req, id, "Elsewhere"); err == nil {
		t.Fatal("repeated application accepted")
	}
	s, item = classificationFixture(t)
	id = addSuggestion(t, s, item)
	os.WriteFile(item.Path, []byte("changed"), 0644)
	if err = s.applySuggestion(req, id, "Forests"); err == nil {
		t.Fatal("stale suggestion accepted")
	}
}

func TestClassificationLocksAndPartialApply(t *testing.T) {
	s, item := classificationFixture(t)
	id := addSuggestion(t, s, item)
	s.lockedCategories = map[string]string{"nature": "secret"}
	if err := s.applySuggestion(httptest.NewRequest("POST", "/", nil), id, "Forests"); err == nil {
		t.Fatal("locked move accepted")
	}
	rec := httptest.NewRecorder()
	s.startClassification(rec, httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"category":"Nature"}`)))
	if rec.Code != 503 {
		t.Fatalf("unconfigured status %d", rec.Code)
	}
	s.lockedCategories = nil
	rec = httptest.NewRecorder()
	s.applyClassification(rec, httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"items":[{"id":999,"subcategory":"Forests"},{"id":1,"subcategory":"Forests"}]}`)))
	var response struct {
		Results []struct {
			Error string `json:"error"`
		} `json:"results"`
	}
	json.Unmarshal(rec.Body.Bytes(), &response)
	if len(response.Results) != 2 || response.Results[0].Error == "" || response.Results[1].Error != "" {
		t.Fatalf("partial apply: %s", rec.Body.String())
	}
}

func TestVisualPreviews(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("FFmpeg unavailable")
	}
	_, item := classificationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	previews, err := visualPreviews(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := base64.StdEncoding.DecodeString(previews[0])
	config, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width > 768 || config.Height > 768 {
		t.Fatalf("preview size %+v %v", config, err)
	}
	path := filepath.Join(t.TempDir(), "short.mp4")
	output, err := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=c=red:s=64x64:d=0.4", "-c:v", "mpeg4", path).CombinedOutput()
	if err != nil {
		t.Fatalf("fixture: %s %v", output, err)
	}
	previews, err = visualPreviews(ctx, video{Path: path, MediaType: "video"})
	if err != nil || len(previews) != 1 {
		t.Fatalf("short video %d %v", len(previews), err)
	}
	for _, duration := range []float64{0.1, 2, 20} {
		times := frameTimes(duration)
		if len(times) > 8 {
			t.Fatal("too many frames")
		}
		for _, at := range times {
			if at <= 0 || at >= duration {
				t.Fatal("frame at edge")
			}
		}
	}
}

func TestClassificationCancellation(t *testing.T) {
	s, item := classificationFixture(t)
	res, _ := s.db.Exec(`INSERT INTO classification_jobs(category,status,total) VALUES('Nature','running',1)`)
	id, _ := res.LastInsertId()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.runClassification(ctx, id, "Nature", []video{item})
	var status string
	s.db.QueryRow(`SELECT status FROM classification_jobs WHERE id=?`, id).Scan(&status)
	if status != "cancelled" {
		t.Fatalf("status %s", status)
	}
}

func TestClassifierResponseValidation(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("FFmpeg unavailable")
	}
	s, item := classificationFixture(t)
	for _, body := range []string{`{"subcategory":"../bad","is_new":true,"abstain":false,"reason":"x"}`, `{"subcategory":"Forest"}`, `not json`} {
		endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		t.Setenv("CLASSIFIER_URL", endpoint.URL)
		_, err := s.classify(context.Background(), item, "Nature")
		endpoint.Close()
		if err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		json.NewDecoder(r.Body).Decode(&payload)
		if _, ok := payload["filename"]; ok {
			t.Error("filename sent")
		}
		if len(payload["images"].([]any)) != 1 {
			t.Error("missing preview")
		}
		w.Write([]byte(`{"subcategory":"","is_new":false,"abstain":true,"reason":"Ambiguous"}`))
	}))
	defer endpoint.Close()
	t.Setenv("CLASSIFIER_URL", endpoint.URL)
	result, err := s.classify(context.Background(), item, "Nature")
	if err != nil || !result.Abstain {
		t.Fatalf("abstention %+v %v", result, err)
	}
}
