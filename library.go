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
)

const legacyTrashDirectory = ".video-library-trash"

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
	title := strings.TrimSpace(req.Title)
	if title == "" {
		return video{}, fmt.Errorf("Title is required.")
	}
	category := item.Category
	if req.Category != nil {
		category = *req.Category
	}
	if req.Subcategory != nil {
		return video{}, fmt.Errorf("Subcategories were removed; omit subcategory.")
	}
	moved, err := moveVideoToLocation(item, category)
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
	_, err = tx.Exec(`UPDATE videos SET title=?,tags=?,path=?,relative_path=?,category=?,filename=?,favorited=?,watch_later=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, title, normalizeTags(req.Tags), moved.Path, moved.RelativePath, moved.Category, moved.Filename, boolToInt(boolValueOrDefault(req.Favorited, item.Favorited)), boolToInt(boolValueOrDefault(req.WatchLater, item.WatchLater)), item.ID)
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
	if req.Subcategory != nil {
		return fmt.Errorf("Subcategories were removed; omit subcategory.")
	}
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
		if req.Category == nil {
			return fmt.Errorf("Choose a destination category.")
		}
	case "tags":
		if req.TagMode != "add" && req.TagMode != "remove" && req.TagMode != "replace" {
			return fmt.Errorf("Choose add, remove, or replace tags.")
		}
	case "favorited", "watch_later":
		if req.Value == nil {
			return fmt.Errorf("Choose an explicit flag value.")
		}
	case "delete":
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
			cat, e := normalizeCategory(*req.Category)
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
			case "delete":
				err = s.permanentDelete(item)
			}
			if err == nil && req.Action != "delete" {
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
func (s *server) deleteFolder(category, subcategory string) (map[string]any, error) {
	items, roots, err := s.categoryContents(category)
	if err != nil {
		return nil, err
	}
	for _, root := range roots {
		if err := checkFolderPath(root, locationDirectory(root, category, subcategory)); err != nil {
			return nil, err
		}
	}
	results := []bulkItemResult{}
	deleted := 0
	for _, item := range items {
		err := s.permanentDelete(item)
		result := bulkItemResult{ID: item.ID, Success: err == nil}
		if err != nil {
			result.Error = err.Error()
		} else {
			deleted++
		}
		results = append(results, result)
	}
	retained := false
	for _, root := range roots {
		dir := locationDirectory(root, category, subcategory)
		// Remove empty immediate subcategories belonging to a deleted category.
		if subcategory == "" {
			entries, err := os.ReadDir(dir)
			if err == nil {
				for _, entry := range entries {
					if entry.IsDir() {
						child := filepath.Join(dir, entry.Name())
						if checkFolderPath(root, child) == nil {
							_ = os.Remove(child)
						}
					}
				}
			}
		}
		// Removal cannot delete any remaining unindexed contents.
		if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
			retained = true
		}
	}
	if err := syncCategories(s.db); err != nil {
		return nil, err
	}
	return map[string]any{"category": category, "deleted": deleted, "results": results, "folder_retained": retained}, nil
}
