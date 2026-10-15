package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type libraryQuerier interface {
	Query(string, ...any) (*sql.Rows, error)
}
type libraryFolder struct {
	category string
}

func discoverLibraryFolders(db libraryQuerier) ([]libraryFolder, error) {
	rows, err := db.Query("SELECT root FROM library_roots UNION SELECT DISTINCT root FROM videos")
	if err != nil {
		return nil, err
	}
	roots := []string{}
	for rows.Next() {
		var root string
		if err := rows.Scan(&root); err != nil {
			rows.Close()
			return nil, err
		}
		roots = append(roots, root)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	folders := []libraryFolder{}
	for _, root := range roots {
		if err := checkFolderPath(root, root); err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() || entry.Name() == legacyTrashDirectory {
				continue
			}
			cat, err := normalizeCategory(entry.Name())
			if err != nil || cat == uncategorizedCategory {
				continue
			}
			dir := filepath.Join(root, entry.Name())
			if err := checkFolderPath(root, dir); err != nil {
				return nil, err
			}
			folder := libraryFolder{category: cat}
			folders = append(folders, folder)
		}
	}
	return folders, nil
}

// WalkDir never follows links. Remove checks the directory is still empty atomically.
func cleanupEmptyDirectories(root string) (int, []string) {
	warnings := []string{}
	dirs := []string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", path, err))
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		if path != root && entry.Name() == legacyTrashDirectory {
			return filepath.SkipDir
		}
		if path != root {
			dirs = append(dirs, path)
		}
		return nil
	})
	if err != nil {
		warnings = append(warnings, err.Error())
	}
	removed := 0
	for i := len(dirs) - 1; i >= 0; i-- {
		dir := dirs[i]
		if err := checkFolderPath(root, dir); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", dir, err))
			continue
		}
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", dir, err))
			continue
		}
		if len(entries) != 0 {
			continue
		}
		if err := os.Remove(dir); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				warnings = append(warnings, fmt.Sprintf("%s: %v", dir, err))
			}
		} else {
			removed++
		}
	}
	return removed, warnings
}

// Only legacy databases have these columns. Each restoration commits separately.
func migrateLegacyTrash(db *sql.DB) error {
	var exists int
	if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('videos') WHERE name='trashed_at'").Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return nil
	}
	if err := ensureColumn(db, "videos", "subcategory", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureColumn(db, "videos", "trash_path", "TEXT"); err != nil {
		return err
	}
	rows, err := db.Query("SELECT id,root,category,subcategory,filename,COALESCE(trash_path,path) FROM videos WHERE trashed_at IS NOT NULL ORDER BY id")
	if err != nil {
		return err
	}
	type legacyItem struct {
		id                             int64
		root, cat, sub, filename, path string
	}
	items := []legacyItem{}
	for rows.Next() {
		var item legacyItem
		if err := rows.Scan(&item.id, &item.root, &item.cat, &item.sub, &item.filename, &item.path); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range items {
		err := func() error {
			cat, sub, err := normalizeCategorySelection(item.cat, item.sub)
			if err != nil {
				return err
			}
			if item.filename != filepath.Base(item.filename) || strings.ContainsAny(item.filename, "/\\") {
				return fmt.Errorf("invalid filename")
			}
			legacyRoot := filepath.Join(item.root, legacyTrashDirectory)
			if err := checkFolderPath(item.root, filepath.Dir(item.path)); err != nil {
				return err
			}
			rel, err := filepath.Rel(legacyRoot, item.path)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				return fmt.Errorf("legacy file outside reserved directory")
			}
			dir := locationDirectory(item.root, cat, sub)
			if err := checkFolderPath(item.root, dir); err != nil {
				return err
			}
			if err := ensureManagedDirectory(dir); err != nil {
				return err
			}
			var source, target string
			err = db.QueryRow("SELECT source,target FROM pending_restorations WHERE id=?", item.id).Scan(&source, &target)
			if errors.Is(err, sql.ErrNoRows) {
				target, err = uniqueTargetPath(dir, item.filename)
				if err != nil {
					return err
				}
				source = item.path
				if _, err = db.Exec("INSERT INTO pending_restorations(id,source,target) VALUES(?,?,?)", item.id, source, target); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
			if source != item.path || filepath.Dir(target) != dir {
				return fmt.Errorf("invalid restoration journal path")
			}
			info, err := os.Lstat(source)
			if err == nil {
				if !info.Mode().IsRegular() {
					return fmt.Errorf("legacy media is not a regular file")
				}
				if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("restoration destination occupied or inaccessible")
				}
				if err := os.Rename(source, target); err != nil {
					return err
				}
			} else if errors.Is(err, os.ErrNotExist) {
				info, err := os.Lstat(target)
				if err != nil {
					return err
				}
				if !info.Mode().IsRegular() {
					return fmt.Errorf("restoration destination is not a regular file")
				}
			} else {
				return err
			}
			tx, err := db.Begin()
			if err != nil {
				return compensateMove(item.id, target, item.path, err)
			}
			defer tx.Rollback()
			_, err = tx.Exec("UPDATE videos SET path=?,relative_path=?,filename=?,category=?,subcategory=?,missing=0,trashed_at=NULL,trash_path=NULL WHERE id=?", target, buildRelativePath(cat, sub, filepath.Base(target)), filepath.Base(target), cat, sub, item.id)
			if err == nil {
				_, err = tx.Exec("DELETE FROM pending_restorations WHERE id=?", item.id)
			}
			if err == nil {
				err = tx.Commit()
			}
			if err != nil {
				tx.Rollback()
				return compensateMove(item.id, target, item.path, err)
			}
			_ = os.Remove(filepath.Dir(item.path))
			_ = os.Remove(legacyRoot)
			return nil
		}()
		if err != nil {
			return fmt.Errorf("legacy Trash migration failed for media %d at %s: %w", item.id, item.path, err)
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, column := range []string{"trashed_at", "trash_path"} {
		if _, err := tx.Exec("ALTER TABLE videos DROP COLUMN " + column); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func categoryNameSet(db libraryQuerier) (map[string]bool, error) {
	rows, err := db.Query("SELECT name FROM categories")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names[name] = true
	}
	return names, rows.Err()
}
