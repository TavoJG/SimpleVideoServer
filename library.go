package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const trashDirectory = ".video-library-trash"

type bulkItemResult struct {
	ID      int64  `json:"id"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	Video   *video `json:"video,omitempty"`
}
type bulkRequest struct {
	IDs         []int64  `json:"ids"`
	Action      string   `json:"action"`
	Category    *string  `json:"category"`
	Subcategory *string  `json:"subcategory"`
	Tags        []string `json:"tags"`
	TagMode     string   `json:"tag_mode"`
	Value       *bool    `json:"value"`
}

// File operations hold libraryMu through the corresponding database transaction.
func compensateMove(id int64, from, to string, cause error) error {
	if from == to {
		return cause
	}
	if err := os.Rename(from, to); err != nil {
		log.Printf("media rollback failed: id=%d from=%q to=%q err=%v", id, from, to, err)
		return fmt.Errorf("%v; file rollback failed for media %d: %v", cause, id, err)
	}
	return cause
}

func (s *server) updateMedia(item video, req updateVideoRequest) (video, error) {
	if item.TrashedAt != "" {
		return video{}, fmt.Errorf("Restore this item before editing it.")
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		return video{}, fmt.Errorf("Title is required.")
	}
	category, subcategory := item.Category, item.Subcategory
	if req.Category != nil {
		category = *req.Category
	}
	if req.Subcategory != nil {
		subcategory = *req.Subcategory
	}
	moved, err := moveVideoToLocation(item, category, subcategory)
	if err != nil {
		return video{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return video{}, compensateMove(item.ID, moved.Path, item.Path, err)
	}
	defer tx.Rollback()
	fail := func(cause error) (video, error) {
		tx.Rollback()
		return video{}, compensateMove(item.ID, moved.Path, item.Path, cause)
	}
	if err := ensureCategoryTx(tx, moved.Category); err != nil {
		return fail(err)
	}
	_, err = tx.Exec(`UPDATE videos SET title=?,tags=?,path=?,relative_path=?,category=?,subcategory=?,filename=?,favorited=?,watch_later=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, title, normalizeTags(req.Tags), moved.Path, moved.RelativePath, moved.Category, moved.Subcategory, moved.Filename, boolToInt(boolValueOrDefault(req.Favorited, item.Favorited)), boolToInt(boolValueOrDefault(req.WatchLater, item.WatchLater)), item.ID)
	if err != nil {
		return fail(err)
	}
	if err = deleteCategoryIfUnusedTx(tx, item.Category); err != nil {
		return fail(err)
	}
	if err = tx.Commit(); err != nil {
		return fail(err)
	}
	return s.queryVideo(item.ID)
}

func (s *server) trashVideo(item video) (video, error) {
	if item.TrashedAt != "" {
		return item, nil
	}
	dir := filepath.Join(item.Root, trashDirectory, fmt.Sprint(item.ID))
	if err := checkFolderPath(item.Root, dir); err != nil {
		return video{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return video{}, err
	}
	target := filepath.Join(dir, item.Filename)
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		return video{}, fmt.Errorf("Trash destination is occupied or inaccessible.")
	}
	moved := false
	if err := os.Rename(item.Path, target); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return video{}, fmt.Errorf("Could not move media to trash: %w", err)
		}
	} else {
		moved = true
	}
	_, err := s.db.Exec(`UPDATE videos SET path=?,trash_path=?,trashed_at=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, target, target, time.Now().UTC().Format(time.RFC3339Nano), item.ID)
	if err != nil {
		if moved {
			err = compensateMove(item.ID, target, item.Path, err)
		}
		return video{}, err
	}
	return s.queryVideo(item.ID)
}

func (s *server) restoreVideo(item video, category, subcategory *string) (video, error) {
	if item.TrashedAt == "" {
		return video{}, fmt.Errorf("This item is not in Trash.")
	}
	cat, sub := item.Category, item.Subcategory
	if category != nil {
		cat = *category
	}
	if subcategory != nil {
		sub = *subcategory
	}
	cat, sub, err := normalizeCategorySelection(cat, sub)
	if err != nil {
		return video{}, err
	}
	dir := locationDirectory(item.Root, cat, sub)
	if err := checkFolderPath(item.Root, dir); err != nil {
		return video{}, err
	}
	if err := ensureManagedDirectory(dir); err != nil {
		return video{}, err
	}
	target, err := uniqueTargetPath(dir, item.Filename)
	if err != nil {
		return video{}, err
	}
	if err := os.Rename(item.TrashPath, target); err != nil {
		return video{}, fmt.Errorf("Trash file is missing or inaccessible: %w", err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return video{}, compensateMove(item.ID, target, item.TrashPath, err)
	}
	defer tx.Rollback()
	fail := func(cause error) (video, error) {
		tx.Rollback()
		return video{}, compensateMove(item.ID, target, item.TrashPath, cause)
	}
	if err := ensureCategoryTx(tx, cat); err != nil {
		return fail(err)
	}
	_, err = tx.Exec(`UPDATE videos SET path=?,relative_path=?,filename=?,category=?,subcategory=?,trashed_at=NULL,trash_path=NULL,missing=0,updated_at=CURRENT_TIMESTAMP WHERE id=?`, target, buildRelativePath(cat, sub, filepath.Base(target)), filepath.Base(target), cat, sub, item.ID)
	if err != nil {
		return fail(err)
	}
	if err = deleteCategoryIfUnusedTx(tx, item.Category); err != nil {
		return fail(err)
	}
	if err = tx.Commit(); err != nil {
		return fail(err)
	}
	_ = os.Remove(filepath.Dir(item.TrashPath))
	return s.queryVideo(item.ID)
}

func (s *server) purgeVideo(item video) error {
	if item.TrashedAt == "" {
		return fmt.Errorf("Only trashed media can be permanently deleted.")
	}
	// Stage removal so database failures can still restore the file.
	stage := item.TrashPath + ".purging"
	if _, err := os.Lstat(stage); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("Pending deletion file is occupied or inaccessible.")
	}
	moved := false
	if err := os.Rename(item.TrashPath, stage); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else {
		moved = true
	}
	tx, err := s.db.Begin()
	if err != nil {
		if moved {
			return compensateMove(item.ID, stage, item.TrashPath, err)
		}
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM videos WHERE id=?", item.ID); err == nil {
		err = deleteCategoryIfUnusedTx(tx, item.Category)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		tx.Rollback()
		if moved {
			return compensateMove(item.ID, stage, item.TrashPath, err)
		}
		return err
	}
	if moved {
		if err := os.Remove(stage); err != nil {
			log.Printf("purge cleanup failed: id=%d path=%q err=%v", item.ID, stage, err)
			// Keep failed removals visible and retryable, including all metadata.
			path := item.TrashPath
			if rollbackErr := os.Rename(stage, path); rollbackErr != nil {
				path = stage
				log.Printf("purge file rollback failed: id=%d from=%q to=%q err=%v", item.ID, stage, item.TrashPath, rollbackErr)
			}
			recovery, recoveryErr := s.db.Begin()
			if recoveryErr == nil {
				recoveryErr = ensureCategoryTx(recovery, item.Category)
				if recoveryErr == nil {
					_, recoveryErr = recovery.Exec(`INSERT INTO videos(id,path,root,relative_path,category,subcategory,media_type,filename,title,tags,size_bytes,mtime,missing,favorited,watch_later,created_at,trashed_at,trash_path) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, item.ID, path, item.Root, item.RelativePath, item.Category, item.Subcategory, item.MediaType, item.Filename, item.Title, normalizeTags(item.Tags), item.SizeBytes, item.Mtime, boolToInt(item.Missing), boolToInt(item.Favorited), boolToInt(item.WatchLater), item.CreatedAt, item.TrashedAt, path)
				}
				if recoveryErr == nil {
					recoveryErr = recovery.Commit()
				} else {
					_ = recovery.Rollback()
				}
			}
			if recoveryErr != nil {
				log.Printf("purge record recovery failed: id=%d path=%q err=%v", item.ID, path, recoveryErr)
				return fmt.Errorf("File cleanup and record recovery failed for media %d at %s: %v; %v", item.ID, path, err, recoveryErr)
			}
			return fmt.Errorf("Permanent deletion failed; item retained in Trash: %w", err)
		}
	}
	_ = os.Remove(filepath.Dir(item.TrashPath))
	return nil
}

func (s *server) listTrash(w http.ResponseWriter, r *http.Request) {
	items, err := s.queryVideos(videoSelectSQL() + " WHERE trashed_at IS NOT NULL ORDER BY trashed_at DESC")
	if err != nil {
		writeError(w, 500, "Could not load Trash.")
		return
	}
	filtered := []video{}
	for _, item := range items {
		if s.canAccessCategory(r, item.Category) {
			filtered = append(filtered, item)
		}
	}
	writeJSON(w, 200, filtered)
}

func (s *server) bulkVideos(w http.ResponseWriter, r *http.Request) {
	var req bulkRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "Invalid JSON body.")
		return
	}
	if err := validateBulk(req); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	s.libraryMu.Lock()
	defer s.libraryMu.Unlock()
	writeJSON(w, 200, map[string]any{"results": s.runBulk(r, req)})
}
func validateBulk(req bulkRequest) error {
	if len(req.IDs) == 0 || len(req.IDs) > 1000 {
		return fmt.Errorf("Choose between 1 and 1000 media items.")
	}
	seen := map[int64]bool{}
	for _, id := range req.IDs {
		if id <= 0 || seen[id] {
			return fmt.Errorf("Media IDs must be positive and unique.")
		}
		seen[id] = true
	}
	switch req.Action {
	case "move":
		if req.Category == nil || req.Subcategory == nil {
			return fmt.Errorf("Choose a category and explicit subcategory destination.")
		}
	case "tags":
		if req.TagMode != "add" && req.TagMode != "remove" && req.TagMode != "replace" {
			return fmt.Errorf("Choose add, remove, or replace tags.")
		}
	case "favorited", "watch_later":
		if req.Value == nil {
			return fmt.Errorf("Choose an explicit flag value.")
		}
	case "trash", "restore", "purge":
	default:
		return fmt.Errorf("Unknown bulk action.")
	}
	return nil
}
func (s *server) runBulk(r *http.Request, req bulkRequest) []bulkItemResult {
	results := []bulkItemResult{}
	for _, id := range req.IDs {
		result := bulkItemResult{ID: id}
		item, err := s.queryVideo(id)
		if err == nil && !s.canAccessCategory(r, item.Category) {
			err = fmt.Errorf("Category is locked.")
		}
		if err == nil && req.Category != nil {
			cat, _, e := normalizeCategorySelection(*req.Category, "")
			if e != nil {
				err = e
			} else if !s.canAccessCategory(r, cat) {
				err = fmt.Errorf("Destination category is locked.")
			}
		}
		if err == nil {
			var updated video
			patch := updateVideoRequest{Title: item.Title, Tags: item.Tags}
			switch req.Action {
			case "move":
				patch.Category = req.Category
				patch.Subcategory = req.Subcategory
				updated, err = s.updateMedia(item, patch)
			case "favorited":
				patch.Favorited = req.Value
				updated, err = s.updateMedia(item, patch)
			case "watch_later":
				patch.WatchLater = req.Value
				updated, err = s.updateMedia(item, patch)
			case "tags":
				tags := splitTags(normalizeTags(req.Tags))
				current := item.Tags
				if req.TagMode == "replace" {
					current = tags
				} else if req.TagMode == "add" {
					current = append(append([]string{}, current...), tags...)
				} else {
					remove := map[string]bool{}
					for _, tag := range tags {
						remove[strings.ToLower(tag)] = true
					}
					kept := []string{}
					for _, tag := range current {
						if !remove[strings.ToLower(tag)] {
							kept = append(kept, tag)
						}
					}
					current = kept
				}
				patch.Tags = current
				updated, err = s.updateMedia(item, patch)
			case "trash":
				updated, err = s.trashVideo(item)
			case "restore":
				updated, err = s.restoreVideo(item, req.Category, req.Subcategory)
			case "purge":
				err = s.purgeVideo(item)
			}
			if err == nil && req.Action != "purge" {
				result.Video = &updated
			}
		}
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				result.Error = "Media no longer exists."
			} else {
				result.Error = err.Error()
			}
		} else {
			result.Success = true
		}
		results = append(results, result)
	}
	return results
}
func (s *server) emptyTrash(w http.ResponseWriter, r *http.Request) {
	s.libraryMu.Lock()
	defer s.libraryMu.Unlock()
	items, err := s.queryVideos(videoSelectSQL() + " WHERE trashed_at IS NOT NULL")
	if err != nil {
		writeError(w, 500, "Could not load Trash.")
		return
	}
	req := bulkRequest{Action: "purge"}
	for _, item := range items {
		if s.canAccessCategory(r, item.Category) {
			req.IDs = append(req.IDs, item.ID)
		}
	}
	writeJSON(w, 200, map[string]any{"results": s.runBulk(r, req)})
}
func (s *server) restoreMedia(w http.ResponseWriter, r *http.Request) {
	s.trashItemAction(w, r, "restore")
}
func (s *server) purgeMedia(w http.ResponseWriter, r *http.Request) { s.trashItemAction(w, r, "purge") }
func (s *server) trashItemAction(w http.ResponseWriter, r *http.Request, action string) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	req := bulkRequest{Action: action, IDs: []int64{id}}
	if action == "restore" && r.ContentLength != 0 {
		if err := readJSON(r, &req); err != nil {
			writeError(w, 400, "Invalid JSON body.")
			return
		}
		req.Action = action
		req.IDs = []int64{id}
	}
	s.libraryMu.Lock()
	defer s.libraryMu.Unlock()
	result := s.runBulk(r, req)[0]
	if !result.Success {
		writeError(w, 400, result.Error)
		return
	}
	writeJSON(w, 200, result)
}

func (s *server) trashFolder(category, subcategory string) (map[string]any, error) {
	query := videoSelectSQL() + " WHERE category=? AND trashed_at IS NULL"
	args := []any{category}
	if subcategory != "" {
		query += " AND subcategory=?"
		args = append(args, subcategory)
	}
	items, err := s.queryVideos(query, args...)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("No indexed media in this folder.")
	}
	results := []bulkItemResult{}
	roots := map[string]bool{}
	deleted := 0
	for _, item := range items {
		roots[item.Root] = true
		updated, e := s.trashVideo(item)
		result := bulkItemResult{ID: item.ID, Success: e == nil}
		if e != nil {
			result.Error = e.Error()
		} else {
			result.Video = &updated
			deleted++
		}
		results = append(results, result)
	}
	retained := false
	for root := range roots {
		dir := locationDirectory(root, category, subcategory)
		if subcategory == "" {
			entries, e := os.ReadDir(dir)
			if e == nil {
				for _, entry := range entries {
					if entry.IsDir() {
						_ = os.Remove(filepath.Join(dir, entry.Name()))
					}
				}
			}
		}
		if e := os.Remove(dir); e != nil && !errors.Is(e, os.ErrNotExist) {
			retained = true
		}
	}
	return map[string]any{"category": category, "deleted": deleted, "results": results, "folder_retained": retained}, nil
}
