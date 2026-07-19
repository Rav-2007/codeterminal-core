package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// skillsSchemaVersion is the schema version this binary knows how to read
// and write. ensureSchema creates it directly on a fresh DB; on an existing
// DB with an older recorded version, this is where future migration steps
// would run (none exist yet, since v1 is the only version so far).
const skillsSchemaVersion = 1

// Skill is one recorded successful solution: what was asked, the steps that
// solved it, and a short human summary. Steps is opaque free-form text (a
// caller may choose to make it a JSON string, but the store never parses
// it); Tags is the one field that genuinely needs structure, so it's
// JSON-encoded into its own column.
type Skill struct {
	ID           string
	CreatedAt    time.Time
	Title        string
	Steps        string
	Tags         []string
	SourcePrompt string
}

// SkillStore is a per-user SQLite-backed store of Skill records. Open once
// and reuse; it holds a single pooled connection for its lifetime.
type SkillStore struct {
	db *sql.DB
}

// DefaultSkillsDBPath returns the conventional per-user location for the
// skills database: ~/.codeterminal/skills.db. It is deliberately per-user,
// not per-workspace — skills are cross-project, unlike the RAG index under
// a workspace's .codeterminal/index.
func DefaultSkillsDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".codeterminal", "skills.db"), nil
}

// OpenSkillStore opens (creating the file and its parent directory if
// absent) the SQLite database at path and ensures its schema is current.
func OpenSkillStore(path string) (*SkillStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("creating skills db directory: %w", err)
	}

	// Refuse a symlinked db FILE: path is a fixed per-user location (not client
	// input), but the driver would follow a symlink planted at skills.db and
	// write SQLite pages through it to an outside file. A leaf lstat is the
	// feasible equivalent of the O_NOFOLLOW the direct-file writers use — the
	// driver owns the actual open, so this does not cover its -wal/-shm sidecar
	// opens (residual noted in the (b)-bucket hardening). A symlinked PARENT dir
	// (a cache deliberately relocated to another disk) is unaffected: lstat only
	// refuses a symlink at the final component.
	if sym, err := leafIsSymlink(path); err != nil {
		return nil, fmt.Errorf("checking skills db path: %w", err)
	} else if sym {
		return nil, fmt.Errorf("skills db %s is a symlink; refusing to open it", path)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening skills db %s: %w", path, err)
	}

	// A single connection sidesteps SQLite's poor concurrent-writer story
	// entirely rather than tuning around it — this store's write volume
	// never justifies a connection pool. WAL + busy_timeout are kept as a
	// second line of defense (e.g. a stale handle from a crashed process).
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return nil, fmt.Errorf("setting journal_mode: %w", err)
	}
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		db.Close()
		return nil, fmt.Errorf("setting busy_timeout: %w", err)
	}

	if err := ensureSchema(db); err != nil {
		db.Close()
		return nil, err
	}

	return &SkillStore{db: db}, nil
}

// Close releases the underlying database handle.
func (s *SkillStore) Close() error {
	return s.db.Close()
}

// ensureSchema creates schema_meta and skills on a fresh database, or reads
// the recorded version on an existing one. It refuses to operate on a DB
// whose recorded version is newer than this binary understands (an older
// binary opening a newer DB), and is the single place future migrations
// (stepping version -> version+1) would be added.
func ensureSchema(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_meta (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("creating schema_meta: %w", err)
	}

	var version int
	err := db.QueryRow(`SELECT version FROM schema_meta LIMIT 1`).Scan(&version)
	switch {
	case err == sql.ErrNoRows:
		if _, err := db.Exec(skillsTableDDL); err != nil {
			return fmt.Errorf("creating skills table: %w", err)
		}
		if _, err := db.Exec(`INSERT INTO schema_meta (version) VALUES (?)`, skillsSchemaVersion); err != nil {
			return fmt.Errorf("recording schema version: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("reading schema version: %w", err)
	case version > skillsSchemaVersion:
		return fmt.Errorf("skills db schema version %d is newer than this binary supports (%d); upgrade codeterminal-daemon", version, skillsSchemaVersion)
	default:
		// version <= skillsSchemaVersion: up to date, or a future migration
		// step-loop would run here to bring it from version up to
		// skillsSchemaVersion. Nothing to do yet at v1.
		return nil
	}
}

const skillsTableDDL = `
CREATE TABLE IF NOT EXISTS skills (
	id            TEXT PRIMARY KEY,
	created_at    TEXT NOT NULL,
	title         TEXT NOT NULL,
	steps         TEXT NOT NULL,
	tags          TEXT NOT NULL DEFAULT '[]',
	source_prompt TEXT NOT NULL DEFAULT ''
)`

// AddSkill inserts skill, generating an ID and CreatedAt if they're zero,
// and returns the ID actually stored.
func (s *SkillStore) AddSkill(ctx context.Context, skill Skill) (string, error) {
	if skill.ID == "" {
		id, err := newSkillID()
		if err != nil {
			return "", fmt.Errorf("generating skill id: %w", err)
		}
		skill.ID = id
	}
	if skill.CreatedAt.IsZero() {
		skill.CreatedAt = time.Now().UTC()
	}

	tagsJSON, err := json.Marshal(skill.Tags)
	if err != nil {
		return "", fmt.Errorf("encoding tags: %w", err)
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO skills (id, created_at, title, steps, tags, source_prompt) VALUES (?, ?, ?, ?, ?, ?)`,
		skill.ID, skill.CreatedAt.Format(time.RFC3339Nano), skill.Title, skill.Steps, string(tagsJSON), skill.SourcePrompt,
	)
	if err != nil {
		return "", fmt.Errorf("inserting skill: %w", err)
	}
	return skill.ID, nil
}

// ListSkills returns up to limit skills, newest first, skipping the first
// offset. rowid is used as a tiebreak so ordering is deterministic even
// when multiple skills share the same created_at.
func (s *SkillStore) ListSkills(ctx context.Context, limit, offset int) ([]Skill, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, created_at, title, steps, tags, source_prompt FROM skills ORDER BY created_at DESC, rowid DESC LIMIT ? OFFSET ?`,
		limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("listing skills: %w", err)
	}
	defer rows.Close()

	var skills []Skill
	for rows.Next() {
		skill, err := scanSkill(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning skill: %w", err)
		}
		skills = append(skills, skill)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing skills: %w", err)
	}
	return skills, nil
}

// CountSkills returns the total number of skills in the store.
func (s *SkillStore) CountSkills(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM skills`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting skills: %w", err)
	}
	return count, nil
}

// GetSkill returns the skill with the given id, or a clear error if it
// doesn't exist.
func (s *SkillStore) GetSkill(ctx context.Context, id string) (Skill, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, created_at, title, steps, tags, source_prompt FROM skills WHERE id = ?`, id,
	)
	skill, err := scanSkill(row)
	if err == sql.ErrNoRows {
		return Skill{}, fmt.Errorf("skill %q not found", id)
	}
	if err != nil {
		return Skill{}, fmt.Errorf("getting skill %q: %w", id, err)
	}
	return skill, nil
}

// DeleteSkill removes the skill with the given id, or returns a clear error
// if it doesn't exist.
func (s *SkillStore) DeleteSkill(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM skills WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting skill %q: %w", id, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("deleting skill %q: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("skill %q not found", id)
	}
	return nil
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows, letting scanSkill
// back both GetSkill (single row) and ListSkills (row iteration).
type rowScanner interface {
	Scan(dest ...any) error
}

func scanSkill(row rowScanner) (Skill, error) {
	var (
		skill     Skill
		createdAt string
		tagsJSON  string
	)
	if err := row.Scan(&skill.ID, &createdAt, &skill.Title, &skill.Steps, &tagsJSON, &skill.SourcePrompt); err != nil {
		return Skill{}, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Skill{}, fmt.Errorf("parsing created_at %q: %w", createdAt, err)
	}
	skill.CreatedAt = parsed
	if err := json.Unmarshal([]byte(tagsJSON), &skill.Tags); err != nil {
		return Skill{}, fmt.Errorf("parsing tags %q: %w", tagsJSON, err)
	}
	return skill, nil
}

// newSkillID generates a stable, opaque 32-hex-character skill ID from 16
// random bytes. Stdlib only — google/uuid is already pulled in transitively
// by modernc.org/sqlite, but adding it as a direct dependency just for this
// isn't worth it when crypto/rand does the job in four lines.
func newSkillID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
