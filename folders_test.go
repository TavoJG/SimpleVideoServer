package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func foldersFixture(t *testing.T) (*server, string) {
	t.Helper()
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "folders.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := initDB(db); err != nil {
		t.Fatal(err)
	}
	return &server{db: db, defaultVideoRoot: root}, root
}

func folderFile(t *testing.T, root, relative string) string {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("media"), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func folderCall(handler http.HandlerFunc, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	handler(w, httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body)))
	return w
}

func folderScan(t *testing.T, s *server, root string) {
	t.Helper()
	if _, err := s.scanFolder(root); err != nil {
		t.Fatal(err)
	}
}

func TestFoldersRelocate(t *testing.T) {
	for _, tc := range []struct {
		name, body, destination string
		move                    bool
	}{
		{"rename", `{"category":"Trips","subcategory":"Summer","name":"Winter"}`, "Trips/Winter", false},
		{"move", `{"category":"Trips","subcategory":"Summer","target_category":"Archive","target_subcategory":"Trips"}`, "Archive/Trips", true},
		{"promote", `{"category":"Trips","subcategory":"Summer","target_category":"Summer","target_subcategory":""}`, "Summer", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, root := foldersFixture(t)
			folderFile(t, root, "Trips/Summer/clip.mp4")
			folderFile(t, root, "Trips/Summer/notes.txt")
			folderFile(t, root, "Trips/Summer/trashed.mp4")
			folderScan(t, s, root)
			if _, err := s.db.Exec(`UPDATE videos SET trashed_at='2026-01-01',trash_path='/trash/item' WHERE filename='trashed.mp4'`); err != nil {
				t.Fatal(err)
			}
			handler := s.renameSubcategory
			if tc.move {
				handler = s.moveSubcategory
			}
			w := folderCall(handler, tc.body)
			if w.Code != 200 {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			for _, file := range []string{"clip.mp4", "notes.txt"} {
				if _, err := os.Stat(filepath.Join(root, tc.destination, file)); err != nil {
					t.Fatal(err)
				}
			}
			var rel, trashedRel, trashPath string
			if err := s.db.QueryRow(`SELECT relative_path FROM videos WHERE filename='clip.mp4'`).Scan(&rel); err != nil {
				t.Fatal(err)
			}
			if rel != filepath.Join(tc.destination, "clip.mp4") {
				t.Fatalf("relative path %q", rel)
			}
			if err := s.db.QueryRow(`SELECT relative_path,trash_path FROM videos WHERE filename='trashed.mp4'`).Scan(&trashedRel, &trashPath); err != nil {
				t.Fatal(err)
			}
			if trashedRel != "Trips/Summer/trashed.mp4" || trashPath != "/trash/item" {
				t.Fatalf("trash changed: %q %q", trashedRel, trashPath)
			}
			assertManagedDirectoryMode(t, filepath.Join(root, tc.destination))
		})
	}
}

func TestFoldersRejectInvalidDestinations(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		move       bool
	}{
		{"traversal", `{"category":"Trips","subcategory":"Summer","name":"../Winter"}`, false},
		{"empty", `{"category":"Trips","subcategory":"Summer","name":""}`, false},
		{"uncategorized", `{"category":"Trips","subcategory":"Summer","target_category":"Uncategorized"}`, true},
		{"third level", `{"category":"Trips","subcategory":"Summer","target_category":"Archive","target_subcategory":"Trips/Summer"}`, true},
		{"same", `{"category":"Trips","subcategory":"Summer","name":"summer"}`, false},
		{"existing", `{"category":"Trips","subcategory":"Summer","name":"Winter"}`, false},
		{"promotion conflict", `{"category":"Trips","subcategory":"Summer","target_category":"Archive"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, root := foldersFixture(t)
			original := folderFile(t, root, "Trips/Summer/clip.mp4")
			folderFile(t, root, "Trips/Winter/unindexed.txt")
			folderFile(t, root, "Archive/other.mp4")
			folderScan(t, s, root)
			handler := s.renameSubcategory
			if tc.move {
				handler = s.moveSubcategory
			}
			w := folderCall(handler, tc.body)
			if w.Code != 400 {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if _, err := os.Stat(original); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFoldersRollbackAllRoots(t *testing.T) {
	s, root := foldersFixture(t)
	second := t.TempDir()
	// Scan through one configured root at a time to populate both roots.
	for _, dir := range []string{root, second} {
		folderFile(t, dir, "Trips/Summer/clip.mp4")
		s.defaultVideoRoot = dir
		folderScan(t, s, dir)
	}
	s.defaultVideoRoot = root
	if _, err := s.db.Exec(`CREATE TRIGGER fail_folder_update BEFORE UPDATE OF path ON videos BEGIN SELECT RAISE(ABORT,'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	w := folderCall(s.moveSubcategory, `{"category":"Trips","subcategory":"Summer","target_category":"Archive","target_subcategory":"Summer"}`)
	if w.Code != 500 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	for _, dir := range []string{root, second} {
		if _, err := os.Stat(filepath.Join(dir, "Trips/Summer/clip.mp4")); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, "Archive")); !os.IsNotExist(err) {
			t.Fatalf("destination retained: %v", err)
		}
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM videos WHERE category='Archive'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("database changed: %d %v", count, err)
	}
}

func TestFoldersUnindexedOnlyAndSymlink(t *testing.T) {
	s, root := foldersFixture(t)
	folderFile(t, root, "Trips/Summer/notes.txt")
	w := folderCall(s.renameSubcategory, `{"category":"Trips","subcategory":"Summer","name":"Winter"}`)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "Trips/Summer")); err != nil {
		t.Fatal(err)
	}
	w = folderCall(s.renameSubcategory, `{"category":"Trips","subcategory":"Summer","name":"Spring"}`)
	if w.Code != 400 {
		t.Fatalf("symlink status %d", w.Code)
	}
}

func TestFoldersLocks(t *testing.T) {
	s, root := foldersFixture(t)
	folderFile(t, root, "Trips/Summer/clip.mp4")
	folderScan(t, s, root)
	s.lockedCategories = map[string]string{"trips": "secret"}
	w := folderCall(s.renameSubcategory, `{"category":"Trips","subcategory":"Summer","name":"Winter"}`)
	if w.Code != 403 {
		t.Fatalf("status %d", w.Code)
	}
	r := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"category":"Trips","subcategory":"Summer","name":"Winter"}`))
	r.AddCookie(&http.Cookie{Name: s.categoryCookieName("Trips"), Value: s.categoryToken("Trips")})
	w = httptest.NewRecorder()
	s.renameSubcategory(w, r)
	if w.Code != 200 {
		t.Fatalf("authorized status %d: %s", w.Code, w.Body.String())
	}
	w = folderCall(s.managedRenameCategory, `{"from":"Trips","to":"Archive"}`)
	if w.Code != 400 {
		t.Fatalf("locked category rename status %d", w.Code)
	}
}

func TestFoldersTrashRetainsUnindexedFiles(t *testing.T) {
	s, root := foldersFixture(t)
	folderFile(t, root, "Trips/Summer/clip.mp4")
	notes := folderFile(t, root, "Trips/Summer/notes.txt")
	folderScan(t, s, root)
	w := folderCall(s.deleteSubcategory, `{"category":"Trips","subcategory":"Summer"}`)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Deleted  int              `json:"deleted"`
		Results  []bulkItemResult `json:"results"`
		Retained bool             `json:"folder_retained"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Deleted != 1 || !response.Retained || len(response.Results) != 1 || !response.Results[0].Success || response.Results[0].Video == nil {
		t.Fatalf("response %+v", response)
	}
	if _, err := os.Stat(notes); err != nil {
		t.Fatal(err)
	}
	var original string
	if err := s.db.QueryRow(`SELECT relative_path FROM videos WHERE trashed_at IS NOT NULL`).Scan(&original); err != nil || original != "Trips/Summer/clip.mp4" {
		t.Fatalf("trash original %q: %v", original, err)
	}
}

func TestFoldersManagedCategories(t *testing.T) {
	for _, rename := range []bool{true, false} {
		t.Run(map[bool]string{true: "rename", false: "move"}[rename], func(t *testing.T) {
			s, root := foldersFixture(t)
			folderFile(t, root, "Trips/clip.mp4")
			folderFile(t, root, "Trips/notes.txt")
			if rename {
				folderFile(t, root, "Trips/Summer/nested.mp4")
			} else {
				folderFile(t, root, "Archive/existing.mp4")
			}
			folderScan(t, s, root)
			handler, body, destination := s.managedRenameCategory, `{"from":"Trips","to":"Archive"}`, "Archive"
			if !rename {
				handler, body, destination = s.managedMoveCategory, `{"from":"Trips","target":"Archive"}`, "Archive/Trips"
			}
			w := folderCall(handler, body)
			if w.Code != 200 {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if _, err := os.Stat(filepath.Join(root, destination, "notes.txt")); err != nil {
				t.Fatal(err)
			}
			assertManagedDirectoryMode(t, filepath.Join(root, destination))
			if rename {
				assertManagedDirectoryMode(t, filepath.Join(root, destination, "Summer"))
			}
		})
	}
}

func TestFoldersTrashPartialFailure(t *testing.T) {
	s, root := foldersFixture(t)
	folderFile(t, root, "Trips/Summer/good.mp4")
	failed := folderFile(t, root, "Trips/Summer/fail.mp4")
	folderScan(t, s, root)
	if _, err := s.db.Exec(`CREATE TRIGGER fail_trash BEFORE UPDATE OF trashed_at ON videos WHEN OLD.filename='fail.mp4' BEGIN SELECT RAISE(ABORT,'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	w := folderCall(s.deleteSubcategory, `{"category":"Trips","subcategory":"Summer"}`)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Deleted  int              `json:"deleted"`
		Results  []bulkItemResult `json:"results"`
		Retained bool             `json:"folder_retained"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Deleted != 1 || !response.Retained || len(response.Results) != 2 {
		t.Fatalf("response %+v", response)
	}
	var failures int
	for _, result := range response.Results {
		if !result.Success {
			failures++
			if result.Error == "" || result.Video != nil {
				t.Fatalf("failure result %+v", result)
			}
		}
	}
	if failures != 1 {
		t.Fatalf("failures %d", failures)
	}
	if _, err := os.Stat(failed); err != nil {
		t.Fatal(err)
	}
}

func TestFoldersTrashRemovesEmptyFolder(t *testing.T) {
	s, root := foldersFixture(t)
	folderFile(t, root, "Trips/Summer/clip.mp4")
	folderScan(t, s, root)
	w := folderCall(s.deleteSubcategory, `{"category":"Trips","subcategory":"Summer"}`)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Retained bool `json:"folder_retained"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Retained {
		t.Fatal("empty folder retained")
	}
	if _, err := os.Stat(filepath.Join(root, "Trips/Summer")); !os.IsNotExist(err) {
		t.Fatalf("folder removal: %v", err)
	}
}

func TestFoldersRejectUnindexedNestedSource(t *testing.T) {
	s, root := foldersFixture(t)
	original := folderFile(t, root, "Trips/Summer/Deeper/notes.txt")
	w := folderCall(s.moveSubcategory, `{"category":"Trips","subcategory":"Summer","target_category":"Archive","target_subcategory":"Summer"}`)
	if w.Code != 400 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(original); err != nil {
		t.Fatal(err)
	}
}
