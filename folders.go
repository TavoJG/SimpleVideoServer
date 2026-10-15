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

// Include the configured root so folders containing only unindexed files work.
func (s *server) categoryContents(category string) ([]video, []string, error) {
	query := `SELECT id, root FROM videos WHERE category=?`
	args := []any{category}
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
	rootRows, err := s.db.Query("SELECT root FROM library_roots")
	if err != nil {
		return nil, nil, err
	}
	for rootRows.Next() {
		var root string
		if err := rootRows.Scan(&root); err != nil {
			rootRows.Close()
			return nil, nil, err
		}
		roots[root] = true
	}
	err = rootRows.Err()
	rootRows.Close()
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

func (s *server) managedMoveCategory(w http.ResponseWriter, r *http.Request) {
	writeError(w, 410, "Category nesting was removed; move individual media to a flat category.")
}
func (s *server) managedRenameCategory(w http.ResponseWriter, r *http.Request) {
	var req renameCategoryRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "Invalid JSON body.")
		return
	}
	from, err := normalizeCategory(req.From)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	to, err := normalizeCategory(req.To)
	if err != nil || from == uncategorizedCategory || to == uncategorizedCategory || strings.EqualFold(from, to) {
		writeError(w, 400, "Choose different managed categories.")
		return
	}
	if s.isCategoryLocked(from) || s.isCategoryLocked(to) {
		writeError(w, 400, "Locked categories cannot be renamed.")
		return
	}
	if !s.requireCategoryAccess(w, r, from) || !s.requireCategoryAccess(w, r, to) {
		return
	}
	s.libraryMu.Lock()
	defer s.libraryMu.Unlock()
	items, roots, err := s.categoryContents(from)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	type move struct{ from, to string }
	moves := []move{}
	originalModes := map[string]os.FileMode{}
	for _, root := range roots {
		source, target := locationDirectory(root, from, ""), locationDirectory(root, to, "")
		if err := checkFolderPath(root, source); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		if err := checkFolderPath(root, target); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
			writeError(w, 400, "Destination exists or is inaccessible.")
			return
		}
		if _, err := os.Lstat(source); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		if err := filepath.WalkDir(source, func(path string, e os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() {
				info, err := e.Info()
				if err != nil {
					return err
				}
				originalModes[path] = info.Mode()
			}
			if e.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("symlinks cannot be moved")
			}
			return nil
		}); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		moves = append(moves, move{source, target})
	}
	if len(moves) == 0 {
		writeError(w, 404, "Category folder does not exist.")
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	defer tx.Rollback()
	moved := []move{}
	fail := func(cause error) {
		tx.Rollback()
		for i := len(moved) - 1; i >= 0; i-- {
			cause = errors.Join(cause, os.Rename(moved[i].to, moved[i].from))
		}
		for path, mode := range originalModes {
			cause = errors.Join(cause, os.Chmod(path, mode))
		}
		writeError(w, 500, cause.Error())
	}
	for _, move := range moves {
		if err := os.Rename(move.from, move.to); err != nil {
			fail(err)
			return
		}
		moved = append(moved, move)
		if err := normalizeManagedDirectoryTree(move.to); err != nil {
			fail(err)
			return
		}
	}
	for _, item := range items {
		rel := buildRelativePath(to, "", item.Filename)
		if _, err := tx.Exec("UPDATE videos SET path=?,relative_path=?,category=?,updated_at=CURRENT_TIMESTAMP WHERE id=?", filepath.Join(item.Root, rel), rel, to, item.ID); err != nil {
			fail(err)
			return
		}
	}
	if err := ensureCategoryTx(tx, from); err != nil {
		fail(err)
		return
	}
	if _, err := tx.Exec("UPDATE categories SET name=?,updated_at=CURRENT_TIMESTAMP WHERE name=?", to, from); err != nil {
		fail(err)
		return
	}
	// Preserve compatibility redirects and potential future inherited locks after rename.
	if _, err := tx.Exec("UPDATE category_migrations SET target=? WHERE target=? COLLATE NOCASE", to, from); err != nil {
		fail(err)
		return
	}
	if _, err := tx.Exec("UPDATE category_lock_aliases SET category=? WHERE category=? COLLATE NOCASE", to, from); err != nil {
		fail(err)
		return
	}
	if err := syncCategoriesTx(tx); err != nil {
		fail(err)
		return
	}
	if err := tx.Commit(); err != nil {
		fail(err)
		return
	}
	if err := s.loadCategoryAliases(); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"from": from, "to": to, "updated": len(items)})
}
