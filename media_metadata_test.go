package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStartupDropsRetiredMediaColumnPreservingCategoryTags(t *testing.T) {
	s := libraryFixture(t)
	id := libraryID(t, s, "clip.jpg")
	original, err := s.queryVideo(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("ALTER TABLE videos ADD COLUMN tags TEXT NOT NULL DEFAULT '[]'"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE videos SET tags='[\"Legacy\"]' WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE categories SET tags='[\"Keep\"]' WHERE name='Travel'"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = initDB(s.db); err != nil {
			t.Fatal(err)
		}
	}
	exists, err := hasColumn(s.db, "tags")
	if err != nil || exists {
		t.Fatalf("retired column retained: %v", err)
	}
	item, err := s.queryVideo(id)
	if err != nil || item.Path != original.Path || item.Title != original.Title || item.CreatedAt != original.CreatedAt {
		t.Fatalf("metadata changed: %+v %v", item, err)
	}
	if got := categoryStoredTags(t, s, "Travel"); got != `["Keep"]` {
		t.Fatalf("category tags changed: %s", got)
	}
	encoded, _ := json.Marshal(item)
	if strings.Contains(string(encoded), `"tags"`) {
		t.Fatal("retired field returned")
	}
}

func TestMediaAPIRejectsRetiredFields(t *testing.T) {
	s := libraryFixture(t)
	id := libraryID(t, s, "clip.jpg")
	for _, payload := range []map[string]any{
		{"title": "Changed", "tags": []string{"Legacy"}},
		{"title": "Changed", "tag_mode": "add"},
	} {
		res := libraryCall(t, s, "PATCH", "/api/videos/"+fmtID(id), payload)
		if res.Code != 400 {
			t.Fatalf("retired field accepted: %d %s", res.Code, res.Body.String())
		}
	}
	for _, payload := range []map[string]any{
		{"ids": []int64{id}, "action": "tags"},
		{"ids": []int64{id}, "action": "favorited", "value": true, "tags": []string{"Legacy"}},
	} {
		res := libraryCall(t, s, "POST", "/api/videos/bulk", payload)
		if res.Code != 400 {
			t.Fatalf("retired bulk operation accepted: %d %s", res.Code, res.Body.String())
		}
	}
	if res := libraryCall(t, s, "GET", "/api/videos?q=clip", nil); res.Code != 200 {
		t.Fatalf("search failed: %d %s", res.Code, res.Body.String())
	}
}
