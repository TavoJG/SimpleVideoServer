package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

const flatMigrationInstruction = "Library has an unmigrated category hierarchy. Stop the server and run video-server migrate-categories --dry-run, then --apply."

func hasColumn(db *sql.DB, column string) (bool, error) {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('videos') WHERE name=?", column).Scan(&count)
	return count != 0, err
}
func hasTable(db *sql.DB, name string) bool {
	var count int
	return db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&count) == nil && count != 0
}

func lockLibraryDatabase(path string, create bool) (*os.File, error) {
	path, err := filepath.Abs(expandHome(path))
	if err != nil {
		return nil, err
	}
	flags := os.O_RDONLY
	if create {
		flags = os.O_RDWR | os.O_CREATE
	}
	file, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("library database is in use; stop the server or other migration: %w", err)
	}
	return file, nil
}

func finishFlatSchema(db *sql.DB) error {
	exists, err := hasColumn(db, "subcategory")
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM videos WHERE subcategory<>''").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New(flatMigrationInstruction)
	}
	_, err = db.Exec("ALTER TABLE videos DROP COLUMN subcategory")
	return err
}

func checkLibraryStartup(db *sql.DB, root string) error {
	var pending, done int
	if err := db.QueryRow("SELECT COUNT(*) FROM category_migrations WHERE done=0").Scan(&pending); err != nil {
		return err
	}
	if pending != 0 {
		return errors.New(flatMigrationInstruction)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM schema_versions WHERE name='flat_categories'").Scan(&done); err != nil {
		return err
	}
	if done == 0 && strings.TrimSpace(root) != "" {
		root, err := filepath.Abs(expandHome(root))
		if err != nil {
			return err
		}
		if err := checkFolderPath(root, root); err != nil {
			return err
		}
		parents, err := os.ReadDir(root)
		if err != nil {
			return err
		}
		for _, parent := range parents {
			if !parent.IsDir() || parent.Name() == legacyTrashDirectory {
				continue
			}
			children, err := os.ReadDir(filepath.Join(root, parent.Name()))
			if err != nil {
				return err
			}
			for _, child := range children {
				if child.IsDir() {
					return errors.New(flatMigrationInstruction)
				}
			}
		}
	}
	if root != "" {
		_, err := db.Exec("INSERT OR IGNORE INTO schema_versions(name) VALUES('flat_categories')")
		return err
	}
	return nil
}

type categoryMigrationStep struct {
	Root        string   `json:"root"`
	Parent      string   `json:"parent"`
	Tag         string   `json:"add_tag"`
	Subcategory string   `json:"subcategory,omitempty"`
	Target      string   `json:"target"`
	Done        bool     `json:"done"`
	Media       int      `json:"affected_media"`
	Locked      bool     `json:"inherits_lock"`
	Warnings    []string `json:"warnings"`
}

func existingMigrationSteps(db *sql.DB, root string) ([]categoryMigrationStep, error) {
	steps := []categoryMigrationStep{}
	if !hasTable(db, "category_migrations") {
		return steps, nil
	}
	rows, err := db.Query("SELECT root,parent,subcategory,target,done FROM category_migrations WHERE root=? ORDER BY parent,subcategory", root)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var step categoryMigrationStep
		if err := rows.Scan(&step.Root, &step.Parent, &step.Subcategory, &step.Target, &step.Done); err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}
	return steps, rows.Err()
}

func planCategoryMigration(db *sql.DB, root string, locks map[string]string) ([]categoryMigrationStep, error) {
	if err := checkFolderPath(root, root); err != nil {
		return nil, err
	}
	persisted, err := existingMigrationSteps(db, root)
	if err != nil {
		return nil, err
	}
	steps := persisted
	if len(steps) == 0 {
		reserved := map[string]bool{categoryKey(uncategorizedCategory): true}
		parents, err := os.ReadDir(root)
		if err != nil {
			return nil, err
		}
		for _, entry := range parents {
			reserved[strings.ToLower(entry.Name())] = true
		}
		if hasTable(db, "categories") {
			rows, err := db.Query("SELECT name FROM categories")
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					rows.Close()
					return nil, err
				}
				reserved[strings.ToLower(name)] = true
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, err
			}
		}
		candidates := map[string]categoryMigrationStep{}
		for _, parent := range parents {
			if !parent.IsDir() || parent.Name() == legacyTrashDirectory {
				continue
			}
			cat, err := normalizeCategory(parent.Name())
			if err != nil || cat == uncategorizedCategory || cat != parent.Name() {
				return nil, fmt.Errorf("invalid parent category %q", parent.Name())
			}
			candidates[cat+"\x00"] = categoryMigrationStep{Root: root, Parent: cat, Target: cat}
			children, err := os.ReadDir(filepath.Join(root, cat))
			if err != nil {
				return nil, err
			}
			for _, child := range children {
				if !child.IsDir() {
					continue
				}
				sub, err := normalizeSubcategory(child.Name())
				if err != nil || sub == "" || sub != child.Name() {
					return nil, fmt.Errorf("invalid subcategory %q", child.Name())
				}
				candidates[cat+"\x00"+sub] = categoryMigrationStep{Root: root, Parent: cat, Subcategory: sub}
			}
		}
		if exists, err := hasColumn(db, "subcategory"); err != nil {
			return nil, err
		} else if exists {
			rows, err := db.Query("SELECT DISTINCT category,subcategory FROM videos WHERE root=?", root)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var cat, sub string
				if err := rows.Scan(&cat, &sub); err != nil {
					rows.Close()
					return nil, err
				}
				if !strings.EqualFold(cat, uncategorizedCategory) {
					reserved[strings.ToLower(cat)] = true
					candidates[cat+"\x00"+sub] = categoryMigrationStep{Root: root, Parent: cat, Subcategory: sub, Target: cat}
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, err
			}
		}
		keys := []string{}
		for key := range candidates {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			step := candidates[key]
			if step.Subcategory != "" {
				name := step.Subcategory
				if reserved[strings.ToLower(name)] || strings.EqualFold(name, uncategorizedCategory) {
					name = step.Parent + " - " + step.Subcategory
				}
				base := name
				for index := 2; reserved[strings.ToLower(name)]; index++ {
					name = fmt.Sprintf("%s_%d", base, index)
				}
				if _, err := normalizeCategory(name); err != nil {
					return nil, err
				}
				reserved[strings.ToLower(name)] = true
				step.Target = name
			}
			steps = append(steps, step)
		}
	}
	legacyColumn, err := hasColumn(db, "subcategory")
	if err != nil {
		return nil, err
	}
	for i := range steps {
		step := &steps[i]
		step.Tag = step.Parent
		step.Warnings = []string{}
		step.Locked = locks[categoryKey(step.Parent)] != "" && step.Subcategory != ""
		source := locationDirectory(root, step.Parent, step.Subcategory)
		if step.Done {
			source = locationDirectory(root, step.Target, "")
		}
		if _, err := os.Lstat(source); errors.Is(err, os.ErrNotExist) && step.Subcategory != "" {
			source = locationDirectory(root, step.Target, "")
		}
		if err := checkFolderPath(root, source); err != nil {
			return nil, err
		}
		paths := map[string]bool{}
		entries, err := os.ReadDir(source)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("symlink cannot be migrated: %s", filepath.Join(source, entry.Name()))
			}
			if entry.IsDir() {
				if step.Subcategory != "" {
					step.Warnings = append(step.Warnings, "Unsupported nested contents retained: "+filepath.Join(source, entry.Name()))
				}
				continue
			}
			if mediaTypeForFilename(entry.Name()) != "" {
				paths[entry.Name()] = true
			}
		}
		// Validate the whole promoted tree before changing anything.
		if step.Subcategory != "" && err == nil {
			if err := filepath.WalkDir(source, func(path string, e os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if e.Type()&os.ModeSymlink != 0 {
					return fmt.Errorf("symlink cannot be migrated: %s", path)
				}
				return nil
			}); err != nil {
				return nil, err
			}
		}
		if legacyColumn && !step.Done {
			rows, err := db.Query("SELECT filename FROM videos WHERE root=? AND category=? AND subcategory=?", root, step.Parent, step.Subcategory)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					rows.Close()
					return nil, err
				}
				paths[name] = true
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, err
			}
		}
		step.Media = len(paths)
	}
	return steps, nil
}

func runCategoryMigration(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("migrate-categories", flag.ContinueOnError)
	flags.SetOutput(out)
	dry := flags.Bool("dry-run", false, "Preview without modifying files or the database")
	apply := flags.Bool("apply", false, "Apply and resume the migration")
	dbPath := flags.String("db", envOrDefault("VIDEO_DB", "videos.sqlite3"), "SQLite database")
	rootValue := flags.String("root", os.Getenv("VIDEO_ROOT"), "Library root")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *dry == *apply {
		return fmt.Errorf("choose exactly one of --dry-run or --apply")
	}
	if strings.TrimSpace(*rootValue) == "" {
		return fmt.Errorf("set VIDEO_ROOT or --root")
	}
	root, err := filepath.Abs(expandHome(*rootValue))
	if err != nil {
		return err
	}
	lock, err := lockLibraryDatabase(*dbPath, *apply)
	if err != nil {
		return err
	}
	defer lock.Close()
	absoluteDB, err := filepath.Abs(expandHome(*dbPath))
	if err != nil {
		return err
	}
	dsn := absoluteDB
	if *dry {
		dsn = (&url.URL{Scheme: "file", Path: absoluteDB, RawQuery: "mode=ro"}).String()
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	if *apply {
		if err := initDatabase(db, true); err != nil {
			return err
		}
	} else {
		if exists, err := hasColumn(db, "trashed_at"); err != nil {
			return err
		} else if exists {
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM videos WHERE trashed_at IS NOT NULL").Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				fmt.Fprintln(out, "Legacy Trash must be restored before folder promotion; --apply restores it first.")
			}
		}
	}
	steps, err := planCategoryMigration(db, root, parseLockedCategories(os.Getenv("APP_LOCKED_CATEGORIES")))
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(steps); err != nil {
		return err
	}
	if *dry {
		return nil
	}
	if err := applyCategoryMigration(db, root, steps); err != nil {
		return err
	}
	fmt.Fprintln(out, "Migration complete. Start the server normally; scans will clean empty parent folders.")
	return nil
}

func applyCategoryMigration(db *sql.DB, root string, steps []categoryMigrationStep) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	for _, step := range steps {
		if _, err := tx.Exec("INSERT OR IGNORE INTO category_migrations(root,parent,subcategory,target,done) VALUES(?,?,?,?,?)", root, step.Parent, step.Subcategory, step.Target, boolToInt(step.Done)); err != nil {
			tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, step := range steps {
		if step.Done {
			continue
		}
		if err := applyCategoryStep(db, step); err != nil {
			return fmt.Errorf("migration %s/%s -> %s failed: %w", step.Parent, step.Subcategory, step.Target, err)
		}
	}
	if err := finishFlatSchema(db); err != nil {
		return err
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO library_roots(root) VALUES(?)", root); err != nil {
		return err
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO schema_versions(name) VALUES('flat_categories')"); err != nil {
		return err
	}
	return syncCategories(db)
}

func applyCategoryStep(db *sql.DB, step categoryMigrationStep) error {
	root := step.Root
	source := locationDirectory(root, step.Parent, step.Subcategory)
	target := locationDirectory(root, step.Target, "")
	if err := checkFolderPath(root, source); err != nil {
		return err
	}
	if err := checkFolderPath(root, target); err != nil {
		return err
	}
	s := &server{db: db}
	items, err := s.queryVideos(videoSelectSQL()+" WHERE root=? AND category=? AND subcategory=?", root, step.Parent, step.Subcategory)
	if err != nil {
		return err
	}
	byName := map[string]bool{}
	for _, item := range items {
		if item.Filename == "" || item.Filename == "." || item.Filename == ".." || strings.ContainsAny(item.Filename, "/\\\x00") || item.Filename != filepath.Base(item.Filename) {
			return fmt.Errorf("invalid filename for media %d", item.ID)
		}
		byName[item.Filename] = true
	}
	moved := false
	if source != target {
		_, sourceErr := os.Lstat(source)
		_, targetErr := os.Lstat(target)
		if sourceErr == nil {
			if !errors.Is(targetErr, os.ErrNotExist) {
				return fmt.Errorf("destination occupied or inaccessible: %s", target)
			}
			if err := os.Rename(source, target); err != nil {
				return err
			}
			moved = true
		} else if !errors.Is(sourceErr, os.ErrNotExist) {
			return sourceErr
		} else if targetErr != nil && !errors.Is(targetErr, os.ErrNotExist) {
			return targetErr
		}
	}
	tx, err := db.Begin()
	if err != nil {
		if moved {
			return compensateMove(0, target, source, err)
		}
		return err
	}
	defer tx.Rollback()
	fail := func(cause error) error {
		tx.Rollback()
		if moved {
			return compensateMove(0, target, source, cause)
		}
		return cause
	}
	for _, item := range items {
		path := filepath.Join(target, item.Filename)
		if _, err := tx.Exec("UPDATE videos SET path=?,relative_path=?,category=?,subcategory='' WHERE id=?", path, buildRelativePath(step.Target, "", item.Filename), step.Target, item.ID); err != nil {
			return fail(fmt.Errorf("media %d: %w", item.ID, err))
		}
	}
	entries, err := os.ReadDir(target)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || mediaTypeForFilename(entry.Name()) == "" || byName[entry.Name()] {
			continue
		}
		rel := buildRelativePath(step.Target, "", entry.Name())
		if _, err := upsertScannedVideo(tx, root, rel, step.Target, entry); err != nil {
			return fail(err)
		}
	}
	if step.Subcategory != "" {
		if _, err := tx.Exec("INSERT OR IGNORE INTO category_lock_aliases(category,parent) VALUES(?,?)", step.Target, step.Parent); err != nil {
			return fail(err)
		}
	}
	if _, err := tx.Exec("UPDATE category_migrations SET done=1 WHERE root=? AND parent=? AND subcategory=?", root, step.Parent, step.Subcategory); err != nil {
		return fail(err)
	}
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	return nil
}

func (s *server) loadCategoryAliases() error {
	rows, err := s.db.Query("SELECT category,parent FROM category_lock_aliases")
	if err != nil {
		return err
	}
	defer rows.Close()
	aliases := map[string]string{}
	for rows.Next() {
		var category, parent string
		if err := rows.Scan(&category, &parent); err != nil {
			return err
		}
		aliases[categoryKey(category)] = parent
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.categoryAliasMu.Lock()
	s.categoryAliases = aliases
	s.categoryAliasMu.Unlock()
	return nil
}
func (s *server) categoryMigrationMappings(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query("SELECT parent,subcategory,target FROM category_migrations WHERE done=1 AND subcategory<>'' ORDER BY parent,subcategory")
	if err != nil {
		writeError(w, 500, "Could not load category migration mappings.")
		return
	}
	defer rows.Close()
	mappings := []map[string]string{}
	for rows.Next() {
		var parent, sub, target string
		if err := rows.Scan(&parent, &sub, &target); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		mappings = append(mappings, map[string]string{"parent": parent, "subcategory": sub, "category": target})
	}
	if err := rows.Err(); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, mappings)
}
