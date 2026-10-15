package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func categoryMigrationFixture(t *testing.T) (*server, string, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := initDatabase(db, true); err != nil {
		t.Fatal(err)
	}
	return &server{db: db, defaultVideoRoot: root}, root, path
}
func legacyMedia(t *testing.T, s *server, category, sub, filename string, missing bool) int64 {
	t.Helper()
	rel := buildRelativePath(category, sub, filename)
	path := filepath.Join(s.defaultVideoRoot, rel)
	if !missing {
		folderFile(t, s.defaultVideoRoot, rel)
	}
	result, err := s.db.Exec(`INSERT INTO videos(path,root,relative_path,category,subcategory,media_type,filename,title,tags,size_bytes,mtime,missing,created_at,updated_at,favorited,watch_later) VALUES(?,?,?,?,?,'video',?,'Saved title',?,5,123,?,'2020-01-01','2021-01-01',1,1)`, path, s.defaultVideoRoot, rel, category, sub, filename, normalizeTags([]string{"Existing"}), boolToInt(missing))
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCategoryMigrationPromotesAndTagsPreservingMetadata(t *testing.T) {
	s, root, _ := categoryMigrationFixture(t)
	id := legacyMedia(t, s, "Travel", "Japan", "clip.mp4", false)
	direct := legacyMedia(t, s, "Travel", "", "direct.mp4", false)
	comma := legacyMedia(t, s, "Art, Music", "Concerts", "song.mp4", false)
	missing := legacyMedia(t, s, "Travel", "Absent", "missing.mp4", true)
	s.db.Exec("UPDATE videos SET tags=? WHERE id=?", normalizeTags([]string{"Existing", "travel"}), id)
	folderFile(t, root, "Travel/Japan/unindexed.jpg")
	notes := folderFile(t, root, "Travel/Japan/notes.txt")
	deeper := folderFile(t, root, "Travel/Japan/Deeper/ignored.mp4")
	folderFile(t, root, "Travel/notes.txt")
	steps, err := planCategoryMigration(s.db, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyCategoryMigration(s.db, root, steps); err != nil {
		t.Fatal(err)
	}
	item, err := s.queryVideo(id)
	if err != nil {
		t.Fatal(err)
	}
	if item.Category != "Japan" || item.Path != filepath.Join(root, "Japan", "clip.mp4") || item.Title != "Saved title" || !item.Favorited || !item.WatchLater || item.CreatedAt != "2020-01-01" || item.Mtime != 123 || strings.Join(item.Tags, "|") != "Existing|travel" {
		t.Fatalf("metadata changed: %+v", item)
	}
	var updated string
	s.db.QueryRow("SELECT updated_at FROM videos WHERE id=?", id).Scan(&updated)
	if updated != "2021-01-01" {
		t.Fatal("timestamp changed")
	}
	directItem, _ := s.queryVideo(direct)
	if directItem.Category != "Travel" || strings.Join(directItem.Tags, "|") != "Existing|Travel" {
		t.Fatal("direct parent tagging", directItem)
	}
	commaItem, _ := s.queryVideo(comma)
	if strings.Join(commaItem.Tags, "|") != "Existing|Art, Music" {
		t.Fatal("comma tag split", commaItem.Tags)
	}
	missingItem, _ := s.queryVideo(missing)
	if !missingItem.Missing || missingItem.Category != "Absent" {
		t.Fatal("missing record lost", missingItem)
	}
	if _, err := os.Stat(filepath.Join(root, "Absent")); !os.IsNotExist(err) {
		t.Fatal("missing folder created")
	}
	for _, old := range []string{notes, deeper} {
		rel, _ := filepath.Rel(filepath.Join(root, "Travel", "Japan"), old)
		if _, err := os.Stat(filepath.Join(root, "Japan", rel)); err != nil {
			t.Fatal("unindexed contents lost", err)
		}
	}
	var tags string
	if err := s.db.QueryRow("SELECT tags FROM videos WHERE filename='unindexed.jpg'").Scan(&tags); err != nil || tags != `["Travel"]` {
		t.Fatal("new media not tagged", tags, err)
	}
	if exists, err := hasColumn(s.db, "subcategory"); err != nil || exists {
		t.Fatal("legacy column retained", err)
	}
	var before int
	s.db.QueryRow("SELECT COUNT(*) FROM videos").Scan(&before)
	if err := initDatabase(s.db, true); err != nil {
		t.Fatal(err)
	}
	repeated, err := planCategoryMigration(s.db, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyCategoryMigration(s.db, root, repeated); err != nil {
		t.Fatal(err)
	}
	var after int
	s.db.QueryRow("SELECT COUNT(*) FROM videos").Scan(&after)
	if after != before {
		t.Fatal("repeat duplicated media")
	}
	if err := checkLibraryStartup(s.db, root); err != nil {
		t.Fatal(err)
	}
}

func TestCategoryMigrationCollisionNamesAndInheritedLocks(t *testing.T) {
	s, root, _ := categoryMigrationFixture(t)
	legacyMedia(t, s, "Travel", "Japan", "one.mp4", false)
	legacyMedia(t, s, "Work", "japan", "two.mp4", false)
	folderFile(t, root, "Japan/existing.mp4")
	folderFile(t, root, "Travel - Japan/notes.txt")
	steps, err := planCategoryMigration(s.db, root, parseLockedCategories("Travel=secret"))
	if err != nil {
		t.Fatal(err)
	}
	targets := map[string]string{}
	for _, step := range steps {
		if step.Subcategory != "" {
			targets[step.Parent] = step.Target
		}
	}
	if targets["Travel"] != "Travel - Japan_2" || targets["Work"] != "Work - japan" {
		t.Fatalf("collision targets: %+v", targets)
	}
	if err := applyCategoryMigration(s.db, root, steps); err != nil {
		t.Fatal(err)
	}
	s.lockedCategories = parseLockedCategories("Travel=secret")
	if err := s.loadCategoryAliases(); err != nil {
		t.Fatal(err)
	}
	if !s.isCategoryLocked(targets["Travel"]) || s.categoryPassword(targets["Travel"]) != "secret" {
		t.Fatal("inherited lock absent")
	}
	s.lockedCategories = parseLockedCategories("Travel=secret;Travel - Japan_2=override")
	if s.categoryPassword(targets["Travel"]) != "override" {
		t.Fatal("explicit lock not preferred")
	}
	res := libraryCall(t, s, "GET", "/api/videos", nil)
	if strings.Contains(res.Body.String(), "one.mp4") {
		t.Fatal("protected media leaked", res.Body.String())
	}
	wire := libraryCall(t, s, "GET", "/api/categories", nil).Body.String()
	if strings.Contains(wire, "subcategories") {
		t.Fatal("obsolete category interface")
	}
}

func TestCategoryMigrationRollbackAndInterruptedFolderRecovery(t *testing.T) {
	s, root, _ := categoryMigrationFixture(t)
	id := legacyMedia(t, s, "Travel", "Japan", "clip.mp4", false)
	steps, err := planCategoryMigration(s.db, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_flat BEFORE UPDATE ON videos WHEN OLD.id=1 BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if err := applyCategoryMigration(s.db, root, steps); err == nil {
		t.Fatal("expected migration failure")
	}
	if _, err := os.Stat(filepath.Join(root, "Travel", "Japan", "clip.mp4")); err != nil {
		t.Fatal("rollback lost source", err)
	}
	var done int
	s.db.QueryRow("SELECT done FROM category_migrations WHERE subcategory='Japan'").Scan(&done)
	if done != 0 {
		t.Fatal("failed folder marked completed")
	}
	s.db.Exec("DROP TRIGGER reject_flat")
	// Simulate a crash between folder rename and the metadata transaction.
	if err := os.Rename(filepath.Join(root, "Travel", "Japan"), filepath.Join(root, "Japan")); err != nil {
		t.Fatal(err)
	}
	resumed, err := planCategoryMigration(s.db, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyCategoryMigration(s.db, root, resumed); err != nil {
		t.Fatal(err)
	}
	item, err := s.queryVideo(id)
	if err != nil || item.Category != "Japan" {
		t.Fatal("interrupted migration lost record", err)
	}
}

func TestCategoryMigrationDryRunDoesNotWriteAndProcessLockRejectsConcurrency(t *testing.T) {
	s, root, path := categoryMigrationFixture(t)
	legacyMedia(t, s, "Travel", "Japan", "clip.mp4", false)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "Travel", "Japan"))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runCategoryMigration([]string{"--dry-run", "--db", path, "--root", root}, &out); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("preview changed database")
	}
	afterInfo, _ := os.Stat(filepath.Join(root, "Travel", "Japan"))
	if !info.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("preview changed folder")
	}
	if !strings.Contains(out.String(), `"target": "Japan"`) {
		t.Fatal("preview omitted destination", out.String())
	}
	lock, err := lockLibraryDatabase(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := runCategoryMigration([]string{"--apply", "--db", path, "--root", root}, &out); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatal("concurrent migration allowed", err)
	}
}

func TestFlatStartupAndObsoleteInterfaces(t *testing.T) {
	s, root, _ := categoryMigrationFixture(t)
	id := legacyMedia(t, s, "Travel", "Japan", "clip.mp4", false)
	if err := initDB(s.db); err == nil || !strings.Contains(err.Error(), "migrate-categories") {
		t.Fatal("legacy startup allowed", err)
	}
	if err := checkLibraryStartup(s.db, root); err == nil {
		t.Fatal("unindexed hierarchy startup allowed")
	}
	steps, err := planCategoryMigration(s.db, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyCategoryMigration(s.db, root, steps); err != nil {
		t.Fatal(err)
	}
	item, err := s.queryVideo(id)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(item)
	if strings.Contains(string(data), "subcategory") {
		t.Fatal("public subcategory field remains")
	}
	if res := libraryCall(t, s, "PATCH", "/api/videos/"+fmtID(id), map[string]any{"title": item.Title, "tags": item.Tags, "subcategory": ""}); res.Code != 400 {
		t.Fatal("obsolete update accepted")
	}
	if res := libraryCall(t, s, "POST", "/api/videos/bulk", map[string]any{"ids": []int64{id}, "action": "move", "category": "Japan", "subcategory": ""}); res.Code != 400 {
		t.Fatal("obsolete bulk move accepted")
	}
	if res := libraryCall(t, s, "POST", "/api/subcategories/rename", nil); res.Code != 410 {
		t.Fatal("obsolete endpoint active")
	}
	mappings := libraryCall(t, s, "GET", "/api/category-migrations", nil)
	if !strings.Contains(mappings.Body.String(), `"category":"Japan"`) {
		t.Fatal("redirect mapping missing")
	}
}

func TestCategoryMigrationRejectsSymlinksBeforeMoves(t *testing.T) {
	s, root, _ := categoryMigrationFixture(t)
	legacyMedia(t, s, "Travel", "Japan", "clip.mp4", false)
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "Travel", "Japan", "external")); err != nil {
		t.Fatal(err)
	}
	if _, err := planCategoryMigration(s.db, root, nil); err == nil {
		t.Fatal("symlink migration accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "Travel", "Japan", "clip.mp4")); err != nil {
		t.Fatal("preview moved files")
	}
}

func TestMigrationCommandApplyAndRepeatDoesNotRegenerateEditedTags(t *testing.T) {
	s, root, path := categoryMigrationFixture(t)
	id := legacyMedia(t, s, "Travel", "Japan", "clip.mp4", false)
	var out bytes.Buffer
	args := []string{"--apply", "--db", path, "--root", root}
	if err := runCategoryMigration(args, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Migration complete") {
		t.Fatal(out.String())
	}
	if _, err := s.db.Exec("UPDATE videos SET tags=? WHERE id=?", normalizeTags([]string{"Edited, Tag"}), id); err != nil {
		t.Fatal(err)
	}
	if err := runCategoryMigration(args, &out); err != nil {
		t.Fatal(err)
	}
	item, err := s.queryVideo(id)
	if err != nil || strings.Join(item.Tags, "|") != "Edited, Tag" {
		t.Fatal("repeat regenerated tags", item, err)
	}
	if err := initDB(s.db); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationConvertsLegacyTagsToJSONWithoutChangingTimestamps(t *testing.T) {
	s, _, _ := categoryMigrationFixture(t)
	id := legacyMedia(t, s, "Travel", "", "clip.mp4", false)
	s.db.Exec("DELETE FROM schema_versions WHERE name='json_tags'")
	s.db.Exec("UPDATE videos SET tags='One, one, Two' WHERE id=?", id)
	if err := migrateTagsJSON(s.db); err != nil {
		t.Fatal(err)
	}
	var tags, updated string
	if err := s.db.QueryRow("SELECT tags,updated_at FROM videos WHERE id=?", id).Scan(&tags, &updated); err != nil || tags != `["One","Two"]` || updated != "2021-01-01" {
		t.Fatal("conversion changed values", tags, updated, err)
	}
	s.db.Exec("UPDATE videos SET tags=? WHERE id=?", normalizeTags([]string{"Art, Music"}), id)
	if err := migrateTagsJSON(s.db); err != nil {
		t.Fatal(err)
	}
	item, err := s.queryVideo(id)
	if err != nil || len(item.Tags) != 1 || item.Tags[0] != "Art, Music" {
		t.Fatal("JSON tags split", item, err)
	}
}

func TestMigrationPreviewRejectsPermissionFailuresBeforeMoves(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permissions")
	}
	s, root, _ := categoryMigrationFixture(t)
	legacyMedia(t, s, "Travel", "Japan", "clip.mp4", false)
	dir := filepath.Join(root, "Travel", "Japan", "Hidden")
	os.Mkdir(dir, 0700)
	if err := os.Chmod(dir, 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	if _, err := planCategoryMigration(s.db, root, nil); err == nil {
		t.Fatal("unreadable tree accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "Travel", "Japan", "clip.mp4")); err != nil {
		t.Fatal("preview moved files")
	}
}

func TestMigrationRecoversOlderNestedDeletionJournal(t *testing.T) {
	s, root, _ := categoryMigrationFixture(t)
	id := legacyMedia(t, s, "Travel", "Japan", "clip.mp4", false)
	item, err := s.queryVideo(id)
	if err != nil {
		t.Fatal(err)
	}
	record, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	json.Unmarshal(record, &legacy)
	legacy["subcategory"] = "Japan"
	record, _ = json.Marshal(legacy)
	stage := filepath.Join(filepath.Dir(item.Path), ".video-library-delete-legacy")
	if err := os.Rename(item.Path, stage); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO pending_deletions(id,stage,record) VALUES(?,?,?)", id, stage, string(record)); err != nil {
		t.Fatal(err)
	}
	s.db.Exec("DELETE FROM videos WHERE id=?", id)
	if err := initDatabase(s.db, true); err != nil {
		t.Fatal(err)
	}
	var sub string
	if err := s.db.QueryRow("SELECT subcategory FROM videos WHERE id=?", id).Scan(&sub); err != nil || sub != "Japan" {
		t.Fatal("legacy hierarchy lost during recovery", sub, err)
	}
	steps, err := planCategoryMigration(s.db, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyCategoryMigration(s.db, root, steps); err != nil {
		t.Fatal(err)
	}
	restored, err := s.queryVideo(id)
	if err != nil || restored.Category != "Japan" || restored.Path != filepath.Join(root, "Japan", "clip.mp4") {
		t.Fatal("recovered file not promoted", restored, err)
	}
}
