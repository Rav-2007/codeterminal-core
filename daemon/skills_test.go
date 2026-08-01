package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTestSkillStore(t *testing.T) *SkillStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "skills.db")
	store, err := OpenSkillStore(path)
	if err != nil {
		t.Fatalf("OpenSkillStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestAddSkill_GetSkillRoundTrip(t *testing.T) {
	store := openTestSkillStore(t)
	ctx := context.Background()

	skill := Skill{
		Title:        "Fixed flaky retry loop",
		Steps:        "1. Added exponential backoff\n2. Capped retries at 5",
		Tags:         []string{"go", "concurrency"},
		SourcePrompt: "why does this retry loop spin forever?",
	}

	id, err := store.AddSkill(ctx, skill)
	if err != nil {
		t.Fatalf("AddSkill: %v", err)
	}
	if id == "" {
		t.Fatal("AddSkill returned an empty id")
	}

	got, err := store.GetSkill(ctx, id)
	if err != nil {
		t.Fatalf("GetSkill: %v", err)
	}

	if got.ID != id {
		t.Errorf("ID = %q, want %q", got.ID, id)
	}
	if got.Title != skill.Title {
		t.Errorf("Title = %q, want %q", got.Title, skill.Title)
	}
	if got.Steps != skill.Steps {
		t.Errorf("Steps = %q, want %q", got.Steps, skill.Steps)
	}
	if got.SourcePrompt != skill.SourcePrompt {
		t.Errorf("SourcePrompt = %q, want %q", got.SourcePrompt, skill.SourcePrompt)
	}
	if strings.Join(got.Tags, ",") != strings.Join(skill.Tags, ",") {
		t.Errorf("Tags = %v, want %v", got.Tags, skill.Tags)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt was not populated")
	}
}

func TestAddSkill_PreservesExplicitIDAndCreatedAt(t *testing.T) {
	store := openTestSkillStore(t)
	ctx := context.Background()

	explicitTime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	id, err := store.AddSkill(ctx, Skill{ID: "explicit-id", CreatedAt: explicitTime, Title: "t", Steps: "s"})
	if err != nil {
		t.Fatalf("AddSkill: %v", err)
	}
	if id != "explicit-id" {
		t.Errorf("id = %q, want %q", id, "explicit-id")
	}

	got, err := store.GetSkill(ctx, id)
	if err != nil {
		t.Fatalf("GetSkill: %v", err)
	}
	if !got.CreatedAt.Equal(explicitTime) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, explicitTime)
	}
}

func TestGetSkill_MissingIDReturnsClearError(t *testing.T) {
	store := openTestSkillStore(t)

	_, err := store.GetSkill(context.Background(), "does-not-exist")
	if err == nil {
		t.Fatal("expected an error for a missing skill id")
	}
	if !strings.Contains(err.Error(), "does-not-exist") || !strings.Contains(err.Error(), "not found") {
		t.Errorf("error %q does not clearly identify a missing skill", err.Error())
	}
}

func TestListSkills_NewestFirstWithLimitAndOffset(t *testing.T) {
	store := openTestSkillStore(t)
	ctx := context.Background()

	base := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if _, err := store.AddSkill(ctx, Skill{
			CreatedAt: base.Add(time.Duration(i) * time.Hour),
			Title:     "skill-" + string(rune('A'+i)),
			Steps:     "steps",
		}); err != nil {
			t.Fatalf("AddSkill %d: %v", i, err)
		}
	}
	// skill-E has the latest CreatedAt, skill-A the earliest. Ordering is asserted
	// on Title below, which is unique per row, so the returned ids are not needed;
	// they used to be collected into a slice nothing ever read.

	all, err := store.ListSkills(ctx, 100, 0)
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("len(all) = %d, want 5", len(all))
	}
	wantOrder := []string{"skill-E", "skill-D", "skill-C", "skill-B", "skill-A"}
	for i, w := range wantOrder {
		if all[i].Title != w {
			t.Errorf("all[%d].Title = %q, want %q", i, all[i].Title, w)
		}
	}

	limited, err := store.ListSkills(ctx, 2, 0)
	if err != nil {
		t.Fatalf("ListSkills limited: %v", err)
	}
	if len(limited) != 2 || limited[0].Title != "skill-E" || limited[1].Title != "skill-D" {
		t.Errorf("limited = %+v, want [skill-E, skill-D]", limited)
	}

	offset, err := store.ListSkills(ctx, 2, 2)
	if err != nil {
		t.Fatalf("ListSkills offset: %v", err)
	}
	if len(offset) != 2 || offset[0].Title != "skill-C" || offset[1].Title != "skill-B" {
		t.Errorf("offset = %+v, want [skill-C, skill-B]", offset)
	}
}

func TestListSkills_SameTimestampBreaksTieByInsertOrder(t *testing.T) {
	store := openTestSkillStore(t)
	ctx := context.Background()

	same := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	firstID, err := store.AddSkill(ctx, Skill{CreatedAt: same, Title: "first", Steps: "s"})
	if err != nil {
		t.Fatalf("AddSkill first: %v", err)
	}
	secondID, err := store.AddSkill(ctx, Skill{CreatedAt: same, Title: "second", Steps: "s"})
	if err != nil {
		t.Fatalf("AddSkill second: %v", err)
	}

	all, err := store.ListSkills(ctx, 100, 0)
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(all) != 2 || all[0].ID != secondID || all[1].ID != firstID {
		t.Errorf("expected newest-inserted (%s) before older (%s), got order %+v", secondID, firstID, all)
	}
}

func TestDeleteSkill_RemovesRow(t *testing.T) {
	store := openTestSkillStore(t)
	ctx := context.Background()

	id, err := store.AddSkill(ctx, Skill{Title: "to be deleted", Steps: "s"})
	if err != nil {
		t.Fatalf("AddSkill: %v", err)
	}

	if err := store.DeleteSkill(ctx, id); err != nil {
		t.Fatalf("DeleteSkill: %v", err)
	}

	if _, err := store.GetSkill(ctx, id); err == nil {
		t.Fatal("expected GetSkill to fail after DeleteSkill")
	}

	all, err := store.ListSkills(ctx, 100, 0)
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("expected no skills after delete, got %d", len(all))
	}
}

func TestDeleteSkill_MissingIDReturnsClearError(t *testing.T) {
	store := openTestSkillStore(t)

	err := store.DeleteSkill(context.Background(), "does-not-exist")
	if err == nil {
		t.Fatal("expected an error deleting a missing skill id")
	}
	if !strings.Contains(err.Error(), "does-not-exist") || !strings.Contains(err.Error(), "not found") {
		t.Errorf("error %q does not clearly identify a missing skill", err.Error())
	}
}

func TestOpenSkillStore_CreatesSchemaOnFreshDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skills.db")
	store, err := OpenSkillStore(path)
	if err != nil {
		t.Fatalf("OpenSkillStore: %v", err)
	}
	defer store.Close()

	var version int
	if err := store.db.QueryRow(`SELECT version FROM schema_meta LIMIT 1`).Scan(&version); err != nil {
		t.Fatalf("reading schema_meta: %v", err)
	}
	if version != skillsSchemaVersion {
		t.Errorf("schema_meta version = %d, want %d", version, skillsSchemaVersion)
	}
}

func TestOpenSkillStore_ReopeningPreservesRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skills.db")
	ctx := context.Background()

	store, err := OpenSkillStore(path)
	if err != nil {
		t.Fatalf("OpenSkillStore (first open): %v", err)
	}
	id, err := store.AddSkill(ctx, Skill{Title: "persisted", Steps: "s"})
	if err != nil {
		t.Fatalf("AddSkill: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := OpenSkillStore(path)
	if err != nil {
		t.Fatalf("OpenSkillStore (second open): %v", err)
	}
	defer reopened.Close()

	got, err := reopened.GetSkill(ctx, id)
	if err != nil {
		t.Fatalf("GetSkill after reopen: %v", err)
	}
	if got.Title != "persisted" {
		t.Errorf("Title = %q, want %q", got.Title, "persisted")
	}
}

func TestDefaultSkillsDBPath_IsPerUserNotPerProject(t *testing.T) {
	path, err := DefaultSkillsDBPath()
	if err != nil {
		t.Fatalf("DefaultSkillsDBPath: %v", err)
	}
	if !strings.HasSuffix(path, filepath.Join(".codeterminal", "skills.db")) {
		t.Errorf("path = %q, want a suffix of .codeterminal/skills.db", path)
	}
	if filepath.Base(filepath.Dir(path)) != ".codeterminal" {
		t.Errorf("path %q is not rooted under a .codeterminal dir", path)
	}
}
