package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanReconcilesFoldersAndPreservesMissingMetadata(t *testing.T) {
	s := libraryFixture(t)
	item, _ := s.queryVideo(libraryID(t, s, "clip.jpg"))
	yes := true
	item, err := s.updateMedia(item, updateVideoRequest{Title: "Keep title", Tags: []string{"Summer"}, Favorited: &yes, WatchLater: &yes})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(item.Path); err != nil {
		t.Fatal(err)
	}
	// The subcategory keeps Travel alive until its media is externally removed.
	if err := os.RemoveAll(filepath.Join(s.defaultVideoRoot, "Hikes")); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"Empty/Sub/Deeper", "Notes/Empty", "Hidden", "Keep/Child/Deeper"} {
		if err := os.MkdirAll(filepath.Join(s.defaultVideoRoot, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(s.defaultVideoRoot, "Notes", "notes.txt"), []byte("keep"), 0600)
	os.WriteFile(filepath.Join(s.defaultVideoRoot, "Hidden", ".keep"), []byte("keep"), 0600)
	os.WriteFile(filepath.Join(s.defaultVideoRoot, "Keep", "Child", "Deeper", "notes.txt"), []byte("keep"), 0600)
	result, err := s.scanFolder(s.defaultVideoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if result.RemovedFolders != 5 || result.RemovedCategories != 2 || result.Missing != 2 || len(result.CleanupWarnings) == 0 {
		t.Fatalf("scan: %+v", result)
	}
	summaries, err := s.queryCategories()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]categorySummary{}
	for _, cat := range summaries {
		names[cat.Name] = cat
	}
	for _, cat := range []string{"Hidden", "Notes", "Keep", "Uncategorized"} {
		if _, ok := names[cat]; !ok {
			t.Fatalf("folder absent: %s %+v", cat, summaries)
		}
	}
	for _, cat := range []string{"Travel", "Empty"} {
		if _, ok := names[cat]; ok {
			t.Fatalf("obsolete category: %s", cat)
		}
	}
	if names["Keep"].Count != 0 {
		t.Fatal("unsupported nested files counted")
	}
	kept, err := s.queryVideo(item.ID)
	if err != nil || !kept.Missing || kept.Title != "Keep title" || !kept.Favorited || !kept.WatchLater || strings.Join(kept.Tags, ",") != "Summer" {
		t.Fatalf("metadata: %+v %v", kept, err)
	}
	if _, err := os.Stat(s.defaultVideoRoot); err != nil {
		t.Fatal("root deleted")
	}
	second, err := s.scanFolder(s.defaultVideoRoot)
	if err != nil || second.RemovedFolders != 0 || second.RemovedCategories != 0 {
		t.Fatalf("non-idempotent scan: %+v %v", second, err)
	}
	if err := initDB(s.db); err != nil {
		t.Fatal(err)
	}
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM categories WHERE name='Travel'").Scan(&count)
	if count != 0 {
		t.Fatal("missing records recreated category")
	}
}

func TestScanFailureDoesNotCleanFoldersAndLinksStayUntouched(t *testing.T) {
	s := libraryFixture(t)
	empty := filepath.Join(s.defaultVideoRoot, "Empty")
	os.Mkdir(empty, 0755)
	outside := t.TempDir()
	os.Mkdir(filepath.Join(outside, "Empty"), 0755)
	os.Symlink(outside, filepath.Join(s.defaultVideoRoot, "Linked"))
	os.WriteFile(filepath.Join(s.defaultVideoRoot, "new.jpg"), []byte("new"), 0600)
	if _, err := s.db.Exec(`CREATE TRIGGER reject_scan BEFORE INSERT ON videos BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.scanFolder(s.defaultVideoRoot); err == nil {
		t.Fatal("expected scan failure")
	}
	if _, err := os.Stat(empty); err != nil {
		t.Fatal("cleanup ran on failed scan")
	}
	s.db.Exec("DROP TRIGGER reject_scan")
	if _, err := s.scanFolder(s.defaultVideoRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "Empty")); err != nil {
		t.Fatal("followed external symlink")
	}
	if info, err := os.Lstat(filepath.Join(s.defaultVideoRoot, "Linked")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("link removed")
	}
}

func addLegacyTrash(t *testing.T, s *server, id int64, move bool) string {
	t.Helper()
	for _, column := range []string{"trashed_at", "trash_path"} {
		if err := ensureColumn(s.db, "videos", column, "TEXT"); err != nil {
			t.Fatal(err)
		}
	}
	item, err := s.queryVideo(id)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(item.Root, legacyTrashDirectory, fmtID(id), item.Filename)
	if move {
		os.MkdirAll(filepath.Dir(path), 0700)
		if err := os.Rename(item.Path, path); err != nil {
			t.Fatal(err)
		}
	} else {
		os.Remove(item.Path)
	}
	if _, err := s.db.Exec("UPDATE videos SET path=?,trashed_at='legacy',trash_path=? WHERE id=?", path, path, id); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertNoTrashColumns(t *testing.T, db *sql.DB) {
	t.Helper()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('videos') WHERE name IN ('trashed_at','trash_path')").Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy columns remain: %d %v", count, err)
	}
}

func TestLegacyTrashMigrationPreservesMetadataCollisionLocksAndResidualFiles(t *testing.T) {
	s := libraryFixture(t)
	id := libraryID(t, s, "secret.jpg")
	item, _ := s.queryVideo(id)
	if _, err := s.db.Exec("UPDATE videos SET title='Saved',tags='Keep',favorited=1,watch_later=1 WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	trash := addLegacyTrash(t, s, id, true)
	os.WriteFile(item.Path, []byte("replacement"), 0600)
	residual := filepath.Join(s.defaultVideoRoot, legacyTrashDirectory, "notes.txt")
	os.WriteFile(residual, []byte("keep"), 0600)
	if err := initDB(s.db); err != nil {
		t.Fatal(err)
	}
	restored, err := s.queryVideo(id)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Filename != "secret_1.jpg" || restored.Title != "Saved" || strings.Join(restored.Tags, ",") != "Keep" || !restored.Favorited || !restored.WatchLater || restored.CreatedAt != item.CreatedAt {
		t.Fatalf("metadata changed: %+v", restored)
	}
	if data, err := os.ReadFile(restored.Path); err != nil || string(data) != "Private/secret.jpg" {
		t.Fatal("restoration bytes changed")
	}
	if _, err := os.Stat(trash); !os.IsNotExist(err) {
		t.Fatal("legacy file retained")
	}
	assertNoTrashColumns(t, s.db)
	if err := initDB(s.db); err != nil {
		t.Fatal("migration not idempotent", err)
	}
	if result, err := s.scanFolder(s.defaultVideoRoot); err != nil || result.Found != 4 {
		t.Fatalf("legacy residual indexed: %+v %v", result, err)
	}
	if _, err := os.Stat(residual); err != nil {
		t.Fatal("residual removed")
	}
}

func TestLegacyTrashMigrationPartialFailureAndRetry(t *testing.T) {
	s := libraryFixture(t)
	id1 := libraryID(t, s, "clip.jpg")
	id2 := libraryID(t, s, "hike.jpg")
	// Process the earlier ID first regardless of scan ordering.
	if id1 > id2 {
		id1, id2 = id2, id1
	}
	addLegacyTrash(t, s, id1, true)
	missing := addLegacyTrash(t, s, id2, false)
	if err := initDB(s.db); err == nil || !strings.Contains(err.Error(), fmtID(id2)) || !strings.Contains(err.Error(), missing) {
		t.Fatalf("missing migration not reported: %v", err)
	}
	first, err := s.queryVideo(id1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.Path); err != nil {
		t.Fatal("successful migration rolled back")
	}
	var trashed string
	if err := s.db.QueryRow("SELECT trashed_at FROM videos WHERE id=?", id2).Scan(&trashed); err != nil || trashed != "legacy" {
		t.Fatal("unresolved record lost")
	}
	os.MkdirAll(filepath.Dir(missing), 0700)
	os.WriteFile(missing, []byte("recovered"), 0600)
	if err := initDB(s.db); err != nil {
		t.Fatal(err)
	}
	assertNoTrashColumns(t, s.db)
}

func TestLegacyMigrationRollbackAndInterruptedMoveRecovery(t *testing.T) {
	s := libraryFixture(t)
	id := libraryID(t, s, "clip.jpg")
	item, _ := s.queryVideo(id)
	source := addLegacyTrash(t, s, id, true)
	if _, err := s.db.Exec(`CREATE TRIGGER reject_restore BEFORE UPDATE ON videos BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if err := initDB(s.db); err == nil {
		t.Fatal("expected migration failure")
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatal("source lost on rollback")
	}
	s.db.Exec("DROP TRIGGER reject_restore")
	var target string
	if err := s.db.QueryRow("SELECT target FROM pending_restorations WHERE id=?", id).Scan(&target); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(source, target); err != nil {
		t.Fatal(err)
	}
	// Simulate interruption after the move, before its database update.
	if err := initDB(s.db); err != nil {
		t.Fatal(err)
	}
	restored, err := s.queryVideo(id)
	if err != nil || restored.Path != item.Path {
		t.Fatalf("interrupted restoration: %+v %v", restored, err)
	}
	assertNoTrashColumns(t, s.db)
}

func TestPendingDeletionRecoveryAcrossInterruption(t *testing.T) {
	for _, state := range []string{"before_move", "after_move", "after_commit", "after_unlink"} {
		t.Run(state, func(t *testing.T) {
			s := libraryFixture(t)
			item, _ := s.queryVideo(libraryID(t, s, "clip.jpg"))
			stage := filepath.Join(filepath.Dir(item.Path), ".video-library-delete-test")
			data, _ := json.Marshal(item)
			if _, err := s.db.Exec("INSERT INTO pending_deletions(id,stage,record) VALUES(?,?,?)", item.ID, stage, string(data)); err != nil {
				t.Fatal(err)
			}
			if state != "before_move" {
				if err := os.Rename(item.Path, stage); err != nil {
					t.Fatal(err)
				}
			}
			if state == "after_commit" || state == "after_unlink" {
				s.db.Exec("DELETE FROM videos WHERE id=?", item.ID)
			}
			if state == "after_unlink" {
				os.Remove(stage)
			}
			if err := initDB(s.db); err != nil {
				t.Fatal(err)
			}
			restored, err := s.queryVideo(item.ID)
			if state == "after_unlink" {
				if err != sql.ErrNoRows {
					t.Fatal("completed deletion resurrected")
				}
			} else {
				if err != nil || restored.Title != item.Title || restored.CreatedAt != item.CreatedAt {
					t.Fatal("recovery lost record", err)
				}
				if _, err := os.Stat(item.Path); err != nil {
					t.Fatal("recovery lost file", err)
				}
			}
			var count int
			s.db.QueryRow("SELECT COUNT(*) FROM pending_deletions").Scan(&count)
			if count != 0 {
				t.Fatal("journal not cleared")
			}
		})
	}
}

func TestCleanupUnreadableDirectoryReportsWarning(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "Keep", "Child", "Unreadable")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0755)
	_, warnings := cleanupEmptyDirectories(root)
	if len(warnings) == 0 {
		t.Fatal("permission failure not reported")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("unreadable folder removed")
	}
}

func TestScanReportsRemovalPermissionFailureAndRetainsCategory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	s := libraryFixture(t)
	dir := filepath.Join(s.defaultVideoRoot, "Retained")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.defaultVideoRoot, 0555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(s.defaultVideoRoot, 0755)
	result, err := s.scanFolder(s.defaultVideoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.CleanupWarnings) == 0 || result.RemovedFolders != 0 {
		t.Fatalf("cleanup failure: %+v", result)
	}
	cats, err := s.queryCategories()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, cat := range cats {
		if cat.Name == "Retained" && cat.Count == 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("failed removal discarded folder category")
	}
}

func TestPendingDeletionRecoveryRetainsJournalOnDatabaseFailure(t *testing.T) {
	s := libraryFixture(t)
	item, _ := s.queryVideo(libraryID(t, s, "clip.jpg"))
	stage := filepath.Join(filepath.Dir(item.Path), ".video-library-delete-test")
	data, _ := json.Marshal(item)
	s.db.Exec("INSERT INTO pending_deletions(id,stage,record) VALUES(?,?,?)", item.ID, stage, string(data))
	if err := os.Rename(item.Path, stage); err != nil {
		t.Fatal(err)
	}
	s.db.Exec("DELETE FROM videos WHERE id=?", item.ID)
	if _, err := s.db.Exec(`CREATE TRIGGER fail_recovery BEFORE INSERT ON videos BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if err := recoverPendingDeletions(s.db); err == nil {
		t.Fatal("expected recovery error")
	}
	if _, err := os.Stat(item.Path); err != nil {
		t.Fatal("recovery lost bytes")
	}
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM pending_deletions").Scan(&count)
	if count != 1 {
		t.Fatal("journal lost")
	}
	s.db.Exec("DROP TRIGGER fail_recovery")
	if err := recoverPendingDeletions(s.db); err != nil {
		t.Fatal(err)
	}
	if restored, err := s.queryVideo(item.ID); err != nil || restored.CreatedAt != item.CreatedAt {
		t.Fatal("recovery lost metadata", err)
	}
}

func TestScanExternalFolderRenameMarksOldRecordMissing(t *testing.T) {
	s := libraryFixture(t)
	original, _ := s.queryVideo(libraryID(t, s, "clip.jpg"))
	if err := os.Rename(filepath.Join(s.defaultVideoRoot, "Travel"), filepath.Join(s.defaultVideoRoot, "Journeys")); err != nil {
		t.Fatal(err)
	}
	result, err := s.scanFolder(s.defaultVideoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 1 || result.Missing != 1 || result.RemovedCategories != 1 {
		t.Fatalf("rename scan: %+v", result)
	}
	old, err := s.queryVideo(original.ID)
	if err != nil || !old.Missing || old.Title != original.Title {
		t.Fatal("old metadata lost", err)
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM videos WHERE category='Journeys' AND missing=0").Scan(&count); err != nil || count != 1 {
		t.Fatalf("renamed folder count %d: %v", count, err)
	}
	cats, err := s.queryCategories()
	if err != nil {
		t.Fatal(err)
	}
	for _, cat := range cats {
		if cat.Name == "Travel" {
			t.Fatal("old category remains")
		}
	}
}
