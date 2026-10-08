package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type subcategoryRequest struct {
	Category          string `json:"category"`
	Subcategory       string `json:"subcategory"`
	Name              string `json:"name"`
	TargetCategory    string `json:"target_category"`
	TargetSubcategory string `json:"target_subcategory"`
}

func (s *server) renameSubcategory(w http.ResponseWriter, r *http.Request) {
	s.relocateSubcategory(w, r, true)
}

func (s *server) moveSubcategory(w http.ResponseWriter, r *http.Request) {
	s.relocateSubcategory(w, r, false)
}

func readSubcategoryRequest(w http.ResponseWriter, r *http.Request) (subcategoryRequest, bool) {
	var req subcategoryRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON body.")
		return req, false
	}
	var err error
	req.Category, err = normalizeCategory(req.Category)
	if err == nil {
		req.Subcategory, err = normalizeSubcategory(req.Subcategory)
	}
	if err != nil || req.Category == uncategorizedCategory || req.Subcategory == "" || strings.EqualFold(req.Subcategory, uncategorizedCategory) {
		writeError(w, http.StatusBadRequest, "Choose a valid category and subcategory.")
		return req, false
	}
	return req, true
}

// Include the configured root so folders containing only unindexed files work.
func (s *server) subcategoryContents(category, subcategory string) ([]video, []string, error) {
	query := `SELECT id, root FROM videos WHERE category=? AND trashed_at IS NULL`
	args := []any{category}
	if subcategory != "" {
		query += ` AND subcategory=?`
		args = append(args, subcategory)
	}
	rows, err := s.db.Query(query+` ORDER BY id`, args...)
	if err != nil {
		return nil, nil, err
	}
	var ids []int64
	roots := map[string]bool{}
	if s.defaultVideoRoot != "" {
		root, err := filepath.Abs(expandHome(s.defaultVideoRoot))
		if err != nil {
			rows.Close()
			return nil, nil, err
		}
		roots[root] = true
	}
	for rows.Next() {
		var id int64
		var root string
		if err := rows.Scan(&id, &root); err != nil {
			rows.Close()
			return nil, nil, err
		}
		ids = append(ids, id)
		roots[root] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	items := make([]video, 0, len(ids))
	for _, id := range ids {
		item, err := s.queryVideo(id)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, item)
	}
	rootList := make([]string, 0, len(roots))
	for root := range roots {
		rootList = append(rootList, root)
	}
	sort.Strings(rootList)
	return items, rootList, nil
}

// Reject symlink components: folder operations must stay inside the library.
func checkFolderPath(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("folder is outside library")
	}
	current := root
	for _, part := range append([]string{""}, strings.Split(rel, string(os.PathSeparator))...) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("folder path must contain real directories")
		}
	}
	return nil
}

func (s *server) relocateSubcategory(w http.ResponseWriter, r *http.Request, rename bool) {
	req, ok := readSubcategoryRequest(w, r)
	if !ok {
		return
	}
	target, sub := req.TargetCategory, req.TargetSubcategory
	if rename {
		target, sub = req.Category, req.Name
	}
	var err error
	target, err = normalizeCategory(target)
	if err == nil {
		sub, err = normalizeSubcategory(sub)
	}
	if err != nil || target == uncategorizedCategory || strings.EqualFold(sub, uncategorizedCategory) || (rename && sub == "") {
		writeError(w, http.StatusBadRequest, "Choose a valid destination.")
		return
	}
	if !s.requireCategoryAccess(w, r, req.Category) || !s.requireCategoryAccess(w, r, target) {
		return
	}
	if !strings.EqualFold(req.Category, target) && (s.isCategoryLocked(req.Category) || s.isCategoryLocked(target)) {
		writeError(w, http.StatusBadRequest, "Locked categories cannot be moved.")
		return
	}
	if strings.EqualFold(req.Category, target) && (sub == "" || strings.EqualFold(req.Subcategory, sub)) {
		writeError(w, http.StatusBadRequest, "Choose a different destination.")
		return
	}
	s.libraryMu.Lock()
	defer s.libraryMu.Unlock()
	s.executeFolderMove(w, req.Category, req.Subcategory, target, sub, false)
}

func (s *server) managedRenameCategory(w http.ResponseWriter, r *http.Request) {
	var req renameCategoryRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "Invalid JSON body.")
		return
	}
	s.managedCategoryMove(w, r, req.From, req.To, true)
}

func (s *server) managedMoveCategory(w http.ResponseWriter, r *http.Request) {
	var req moveCategoryRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "Invalid JSON body.")
		return
	}
	s.managedCategoryMove(w, r, req.From, req.Target, false)
}

func (s *server) managedCategoryMove(w http.ResponseWriter, r *http.Request, from, target string, rename bool) {
	var err error
	from, err = normalizeCategory(from)
	if err == nil {
		target, err = normalizeCategory(target)
	}
	if err != nil || from == uncategorizedCategory || target == uncategorizedCategory || strings.EqualFold(from, target) {
		writeError(w, 400, "Choose different valid categories.")
		return
	}
	if s.isCategoryLocked(from) || s.isCategoryLocked(target) {
		writeError(w, 400, "Locked categories cannot be renamed or moved.")
		return
	}
	if !s.requireCategoryAccess(w, r, from) || !s.requireCategoryAccess(w, r, target) {
		return
	}
	s.libraryMu.Lock()
	defer s.libraryMu.Unlock()
	sub := ""
	if !rename {
		var count int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM categories WHERE name=? COLLATE NOCASE`, target).Scan(&count); err != nil {
			writeError(w, 500, "Could not check destination.")
			return
		}
		if count == 0 {
			writeError(w, 400, "Destination category does not exist.")
			return
		}
		sub = from
	}
	s.executeFolderMove(w, from, "", target, sub, rename)
}

// The caller holds libraryMu across filesystem moves and the database transaction.
func (s *server) executeFolderMove(w http.ResponseWriter, category, sourceSub, target, sub string, categoryRename bool) {
	req := subcategoryRequest{Category: category, Subcategory: sourceSub}
	items, roots, err := s.subcategoryContents(req.Category, req.Subcategory)
	if err != nil {
		writeError(w, 500, "Could not load subcategory.")
		return
	}
	var count int
	if sub == "" {
		err = s.db.QueryRow(`SELECT COUNT(*) FROM categories WHERE name=? COLLATE NOCASE`, target).Scan(&count)
	} else {
		err = s.db.QueryRow(`SELECT COUNT(*) FROM videos WHERE category=? COLLATE NOCASE AND subcategory=? COLLATE NOCASE AND trashed_at IS NULL`, target, sub).Scan(&count)
	}
	if err != nil {
		writeError(w, 500, "Could not check destination.")
		return
	}
	if count > 0 {
		writeError(w, 400, "Destination already exists.")
		return
	}
	type folderMove struct{ from, to string }
	var moves []folderMove
	originalModes := map[string]os.FileMode{}
	for _, root := range roots {
		from, to := locationDirectory(root, req.Category, req.Subcategory), locationDirectory(root, target, sub)
		if err := checkFolderPath(root, from); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		if err := checkFolderPath(root, to); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		if _, err := os.Lstat(to); !errors.Is(err, os.ErrNotExist) {
			writeError(w, 400, "Destination already exists or is inaccessible.")
			return
		}
		entries, err := os.ReadDir(from)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			writeError(w, 500, "Could not read subcategory.")
			return
		}
		for _, entry := range entries {
			if (!categoryRename && entry.IsDir()) || entry.Type()&os.ModeSymlink != 0 {
				writeError(w, 400, "Only flat categories can be moved; categories with subcategories, nested folders, or symlinks cannot be moved.")
				return
			}
		}
		if categoryRename {
			if err := filepath.WalkDir(from, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.Type()&os.ModeSymlink != 0 {
					return fmt.Errorf("symlinks cannot be moved")
				}
				return nil
			}); err != nil {
				writeError(w, 400, err.Error())
				return
			}
		}
		moves = append(moves, folderMove{from, to})
		if err := filepath.WalkDir(from, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				info, err := entry.Info()
				if err != nil {
					return err
				}
				originalModes[path] = info.Mode()
			}
			return nil
		}); err != nil {
			writeError(w, 500, "Could not read folder permissions.")
			return
		}
		if info, err := os.Stat(filepath.Dir(to)); err == nil {
			originalModes[filepath.Dir(to)] = info.Mode()
		}
	}
	if sourceSub == "" && !categoryRename {
		for _, item := range items {
			if item.Subcategory != "" {
				writeError(w, 400, "Only flat categories can be moved; categories with subcategories cannot be moved.")
				return
			}
		}
	}
	if len(moves) == 0 {
		writeError(w, 404, "Subcategory folder does not exist.")
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, 500, "Could not update subcategory.")
		return
	}
	defer tx.Rollback()
	var moved []folderMove
	var created []string
	rollback := func(cause error) {
		for i := len(moved) - 1; i >= 0; i-- {
			cause = errors.Join(cause, os.Rename(moved[i].to, moved[i].from))
		}
		for i := len(created) - 1; i >= 0; i-- {
			cause = errors.Join(cause, os.Remove(created[i]))
		}
		for path, mode := range originalModes {
			cause = errors.Join(cause, os.Chmod(path, mode))
		}
		writeError(w, 500, fmt.Sprintf("Could not move subcategory: %v", cause))
	}
	for _, move := range moves {
		parent := filepath.Dir(move.to)
		if _, err := os.Stat(parent); errors.Is(err, os.ErrNotExist) {
			if err := ensureManagedDirectory(parent); err != nil {
				rollback(err)
				return
			}
			created = append(created, parent)
		}
		if sub != "" {
			if err := ensureManagedDirectory(parent); err != nil {
				rollback(err)
				return
			}
		}
		if err := os.Rename(move.from, move.to); err != nil {
			rollback(err)
			return
		}
		moved = append(moved, move)
		if err := normalizeManagedDirectoryTree(move.to); err != nil {
			rollback(err)
			return
		}
	}
	for _, item := range items {
		newSub := sub
		if categoryRename {
			newSub = item.Subcategory
		}
		rel := buildRelativePath(target, newSub, item.Filename)
		_, err = tx.Exec(`UPDATE videos SET path=?,relative_path=?,category=?,subcategory=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND trashed_at IS NULL`, filepath.Join(item.Root, rel), rel, target, newSub, item.ID)
		if err != nil {
			rollback(err)
			return
		}
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO categories(name) VALUES(?)`, target); err != nil {
		rollback(err)
		return
	}
	if sourceSub == "" {
		if err = deleteCategoryIfUnusedTx(tx, category); err != nil {
			rollback(err)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		rollback(err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": category, "to": target, "target": target, "category": target, "subcategory": sub, "updated": len(items)})
}

func (s *server) deleteSubcategory(w http.ResponseWriter, r *http.Request) {
	req, ok := readSubcategoryRequest(w, r)
	if !ok || !s.requireCategoryAccess(w, r, req.Category) {
		return
	}
	s.libraryMu.Lock()
	defer s.libraryMu.Unlock()
	items, roots, err := s.subcategoryContents(req.Category, req.Subcategory)
	if err != nil {
		writeError(w, 500, "Could not load subcategory.")
		return
	}
	results := make([]bulkItemResult, 0, len(items))
	deleted := 0
	retained := false
	failedRoots := map[string]bool{}
	for _, root := range roots {
		if err := checkFolderPath(root, locationDirectory(root, req.Category, req.Subcategory)); err != nil {
			writeError(w, 400, err.Error())
			return
		}
	}
	for _, item := range items {
		result := bulkItemResult{ID: item.ID}
		trashed, err := s.trashVideo(item)
		if err != nil {
			result.Error = err.Error()
			retained = true
			failedRoots[item.Root] = true
		} else {
			result.Success = true
			result.Video = &trashed
			deleted++
		}
		results = append(results, result)
	}
	for _, root := range roots {
		if failedRoots[root] {
			continue
		}
		// Remove only empty folders; unindexed content is never deleted.
		err := os.Remove(locationDirectory(root, req.Category, req.Subcategory))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			retained = true
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted, "results": results, "folder_retained": retained})
}
