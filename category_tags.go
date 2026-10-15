package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
)

// A mapping is seeded once, even if its target is subsequently renamed or edited.
func seedCategoryTagsTx(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT m.root,m.parent,m.subcategory,c.name,c.tags FROM category_migrations m JOIN categories c ON c.name=m.target COLLATE NOCASE WHERE m.done=1 AND m.parent<>'Uncategorized' COLLATE NOCASE AND NOT EXISTS(SELECT 1 FROM category_tag_seeds s WHERE s.root=m.root AND s.parent=m.parent AND s.subcategory=m.subcategory) ORDER BY m.root,m.parent,m.subcategory`)
	if err != nil {
		return err
	}
	type seed struct{ root, parent, sub, category, tags string }
	seeds := []seed{}
	for rows.Next() {
		var item seed
		if err := rows.Scan(&item.root, &item.parent, &item.sub, &item.category, &item.tags); err != nil {
			rows.Close()
			return err
		}
		seeds = append(seeds, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range seeds {
		// Read the latest tags because several mappings can target the same category.
		var current string
		if err := tx.QueryRow("SELECT tags FROM categories WHERE name=?", item.category).Scan(&current); err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE categories SET tags=? WHERE name=?", normalizeTags(append(splitTags(current), item.parent)), item.category); err != nil {
			return err
		}
		if _, err := tx.Exec("INSERT INTO category_tag_seeds(root,parent,subcategory) VALUES(?,?,?)", item.root, item.parent, item.sub); err != nil {
			return err
		}
	}
	return nil
}

func (s *server) updateCategoryTags(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Category string    `json:"category"`
		Tags     *[]string `json:"tags"`
	}
	if err := readJSON(r, &req); err != nil || req.Tags == nil {
		writeError(w, 400, "Provide category and a tags array.")
		return
	}
	category, err := normalizeCategory(req.Category)
	if err != nil || strings.TrimSpace(req.Category) == "" {
		writeError(w, 400, "Choose a category.")
		return
	}
	for _, tag := range *req.Tags {
		if strings.ContainsRune(tag, 0) {
			writeError(w, 400, "Tags cannot contain NUL characters.")
			return
		}
	}
	if !s.requireCategoryAccess(w, r, category) {
		return
	}
	s.libraryMu.Lock()
	defer s.libraryMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, 500, "Could not update category tags.")
		return
	}
	defer tx.Rollback()
	folders, err := discoverLibraryFolders(tx)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	exists := category == uncategorizedCategory
	for _, folder := range folders {
		if strings.EqualFold(category, folder.category) {
			category = folder.category
			exists = true
			break
		}
	}
	if !exists {
		writeError(w, 404, "Category no longer exists.")
		return
	}
	if err := ensureCategoryTx(tx, category); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	// Seed first so an explicit edit can remove the initial tag permanently.
	if err := seedCategoryTagsTx(tx); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	tags := normalizeTags(*req.Tags)
	if _, err := tx.Exec("UPDATE categories SET tags=?,updated_at=CURRENT_TIMESTAMP WHERE name=?", tags, category); err != nil {
		writeError(w, 500, fmt.Sprintf("Could not update category tags: %v", err))
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "Could not save category tags.")
		return
	}
	writeJSON(w, 200, map[string]any{"category": category, "tags": splitTags(tags)})
}
