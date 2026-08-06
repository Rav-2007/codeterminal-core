package main

import (
	"context"
	"io"
	"log"
	"path/filepath"
	"testing"
)

func TestRunSkillsCommand(t *testing.T) {
	logger := log.New(io.Discard, "", 0)

	// No args error
	if err := runSkillsCommand(nil, logger); err == nil {
		t.Fatal("expected error on nil args")
	}

	// Unknown subcommand error
	if err := runSkillsCommand([]string{"invalid"}, logger); err == nil {
		t.Fatal("expected error on invalid subcommand")
	}

	// Setup temp skills db
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "skills.db")

	store, err := OpenSkillStore(dbPath)
	if err != nil {
		t.Fatalf("OpenSkillStore: %v", err)
	}
	skillID, err := store.AddSkill(context.Background(), Skill{
		Title: "Test Skill",
		Steps: "Do testing",
		Tags:  []string{"go", "test"},
	})
	if err != nil {
		t.Fatalf("AddSkill: %v", err)
	}
	store.Close()

	// Run list with --db and --json
	err = runSkillsCommand([]string{"list", "--db", dbPath, "--json"}, logger)
	if err != nil {
		t.Fatalf("runSkillsListCommand json error: %v", err)
	}

	// Run list with --db table
	err = runSkillsCommand([]string{"list", "--db", dbPath}, logger)
	if err != nil {
		t.Fatalf("runSkillsListCommand table error: %v", err)
	}

	// Run delete
	err = runSkillsCommand([]string{"delete", "--db", dbPath, skillID}, logger)
	if err != nil {
		t.Fatalf("runSkillsDeleteCommand error: %v", err)
	}

	// Verify count is 0
	store, _ = OpenSkillStore(dbPath)
	defer store.Close()
	count, _ := store.CountSkills(context.Background())
	if count != 0 {
		t.Fatalf("expected count 0 after delete, got %d", count)
	}
}

func TestPrintSkillsTableEmpty(t *testing.T) {
	// Should print "no skills recorded" without panicking
	printSkillsTable(nil)
}

func TestResolveSkillsDBPath(t *testing.T) {
	if p, err := resolveSkillsDBPath("/custom/path"); err != nil || p != "/custom/path" {
		t.Errorf("resolveSkillsDBPath = %s, %v; want /custom/path, nil", p, err)
	}
}
