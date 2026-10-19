package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// The durable journal keeps staged files recoverable across a process interruption.
func (s *server) permanentDelete(item video) error {
	if err := checkFolderPath(item.Root, filepath.Dir(item.Path)); err != nil {
		return err
	}
	if item.Path != filepath.Join(item.Root, item.RelativePath) {
		return fmt.Errorf("media path does not match library location")
	}
	info, err := os.Lstat(item.Path)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return err
	}
	if !missing && !info.Mode().IsRegular() {
		return fmt.Errorf("only regular media files can be deleted")
	}
	data, err := json.Marshal(item)
	if err != nil {
		return err
	}
	stage := ""
	if !missing {
		f, err := os.CreateTemp(filepath.Dir(item.Path), ".video-library-delete-*")
		if err != nil {
			return err
		}
		stage = f.Name()
		if err := f.Close(); err != nil {
			os.Remove(stage)
			return err
		}
		if err := os.Remove(stage); err != nil {
			return err
		}
		// Persist recovery information before moving the file.
		if _, err := s.db.Exec("INSERT INTO pending_deletions(id,stage,record) VALUES(?,?,?)", item.ID, stage, string(data)); err != nil {
			return err
		}
		if err := os.Rename(item.Path, stage); err != nil {
			_, cleanupErr := s.db.Exec("DELETE FROM pending_deletions WHERE id=?", item.ID)
			return errors.Join(err, cleanupErr)
		}
	}
	rollback := func(cause error) error {
		if stage != "" {
			if err := os.Rename(stage, item.Path); err != nil {
				return fmt.Errorf("%v; media %d retained at %s: %w", cause, item.ID, stage, err)
			}
			_, err := s.db.Exec("DELETE FROM pending_deletions WHERE id=?", item.ID)
			return errors.Join(cause, err)
		}
		return cause
	}
	tx, err := s.db.Begin()
	if err != nil {
		return rollback(err)
	}
	defer tx.Rollback()
	_, err = tx.Exec("DELETE FROM videos WHERE id=?", item.ID)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		tx.Rollback()
		return rollback(err)
	}
	if stage != "" {
		if err := os.Remove(stage); err != nil {
			recoveryErr := recoverPendingDeletions(s.db)
			return fmt.Errorf("permanent deletion failed for media %d: %w", item.ID, errors.Join(err, recoveryErr))
		}
		if _, err := s.db.Exec("DELETE FROM pending_deletions WHERE id=?", item.ID); err != nil {
			return fmt.Errorf("file deleted; journal cleanup failed: %w", err)
		}
	}
	return nil
}

func recoverPendingDeletions(db *sql.DB) error {
	rows, err := db.Query("SELECT id,stage,record FROM pending_deletions ORDER BY id")
	if err != nil {
		return err
	}
	type pending struct {
		id          int64
		stage, data string
	}
	items := []pending{}
	for rows.Next() {
		var item pending
		if err := rows.Scan(&item.id, &item.stage, &item.data); err != nil {
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
	legacyColumn, err := hasColumn(db, "subcategory")
	if err != nil {
		return err
	}
	for _, pending := range items {
		var item video
		if err := json.Unmarshal([]byte(pending.data), &item); err != nil {
			return err
		}
		var legacy struct {
			Subcategory *string `json:"subcategory"`
		}
		if err := json.Unmarshal([]byte(pending.data), &legacy); err != nil {
			return err
		}
		if err := checkFolderPath(item.Root, filepath.Dir(item.Path)); err != nil {
			return err
		}
		if filepath.Dir(pending.stage) != filepath.Dir(item.Path) {
			return fmt.Errorf("invalid deletion journal path")
		}
		_, err := os.Lstat(pending.stage)
		if err == nil {
			if _, err := os.Lstat(item.Path); !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("cannot recover media %d: original path occupied or inaccessible", item.ID)
			}
			if err := os.Rename(pending.stage, item.Path); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		// If unlink completed before interruption, no file remains to restore.
		if _, err := os.Lstat(item.Path); err == nil {
			_, err = tx.Exec(`INSERT OR IGNORE INTO videos(id,path,root,relative_path,category,media_type,filename,title,size_bytes,mtime,missing,favorited,watch_later,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, item.ID, item.Path, item.Root, item.RelativePath, item.Category, item.MediaType, item.Filename, item.Title, item.SizeBytes, item.Mtime, boolToInt(item.Missing), boolToInt(item.Favorited), boolToInt(item.WatchLater), item.CreatedAt)
			if err == nil && legacyColumn && legacy.Subcategory != nil {
				_, err = tx.Exec("UPDATE videos SET subcategory=? WHERE id=?", *legacy.Subcategory, item.ID)
			}
			if err != nil {
				tx.Rollback()
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			tx.Rollback()
			return err
		}
		_, err = tx.Exec("DELETE FROM pending_deletions WHERE id=?", item.ID)
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			tx.Rollback()
			return err
		}
	}
	return nil
}
