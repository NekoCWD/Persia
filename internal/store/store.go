// Package store persists Persia's state in SQLite.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store is a SQLite-backed state store.
type Store struct {
	db *sql.DB
}

// Item is a previously seen issue or pull request.
type Item struct {
	Source    string
	RepoKey   string
	Kind      string
	Number    int
	State     string
	Title     string
	Comments  int
	UpdatedAt time.Time
}

// Pipeline is a previously seen CI run.
type Pipeline struct {
	Source    string
	RepoKey   string
	ID        string
	Name      string
	Status    string
	URL       string
	Ref       string
	SHA       string
	UpdatedAt time.Time
}

// Mirror is the last known state of a push mirror.
type Mirror struct {
	RepoKey     string
	Target      string
	Address     string
	PublicKey   string
	LastError   string
	LastUpdated time.Time
}

// Open opens (and migrates) the SQLite database.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}

	dsn := path
	if path != ":memory:" {
		dsn = "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS rooms (
	repo_key   TEXT PRIMARY KEY,
	room_id    TEXT NOT NULL,
	created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS items (
	source     TEXT NOT NULL,
	repo_key   TEXT NOT NULL,
	kind       TEXT NOT NULL,
	number     INTEGER NOT NULL,
	state      TEXT NOT NULL,
	title      TEXT NOT NULL,
	comments   INTEGER NOT NULL DEFAULT 0,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (source, repo_key, kind, number)
);

CREATE TABLE IF NOT EXISTS pipelines (
	source     TEXT NOT NULL,
	repo_key   TEXT NOT NULL,
	id         TEXT NOT NULL,
	name       TEXT NOT NULL,
	status     TEXT NOT NULL,
	url        TEXT NOT NULL,
	ref        TEXT NOT NULL,
	sha        TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (source, repo_key, id)
);

CREATE TABLE IF NOT EXISTS mirrors (
	repo_key    TEXT NOT NULL,
	target      TEXT NOT NULL,
	address     TEXT NOT NULL,
	public_key  TEXT NOT NULL,
	last_error  TEXT NOT NULL,
	last_updated TEXT NOT NULL,
	PRIMARY KEY (repo_key, target)
);

CREATE TABLE IF NOT EXISTS seeds (
	stream     TEXT PRIMARY KEY,
	seeded_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS room_props (
	room_id    TEXT PRIMARY KEY,
	alias      TEXT NOT NULL DEFAULT '',
	public     INTEGER NOT NULL DEFAULT 0,
	updated_at TEXT NOT NULL
);
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	return nil
}

// Room returns the Matrix room ID for a repository, if one was created.
func (s *Store) Room(ctx context.Context, repoKey string) (string, bool, error) {
	var roomID string
	err := s.db.QueryRowContext(ctx, `SELECT room_id FROM rooms WHERE repo_key = ?`, repoKey).Scan(&roomID)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get room: %w", err)
	}
	return roomID, true, nil
}

// SaveRoom stores the Matrix room ID for a repository.
func (s *Store) SaveRoom(ctx context.Context, repoKey, roomID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO rooms (repo_key, room_id, created_at) VALUES (?, ?, ?)
		 ON CONFLICT(repo_key) DO UPDATE SET room_id = excluded.room_id`,
		repoKey, roomID, time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("save room: %w", err)
	}
	return nil
}

// GetItem returns a previously seen issue or pull request.
func (s *Store) GetItem(ctx context.Context, source, repoKey, kind string, number int) (Item, bool, error) {
	var item Item
	var updated string
	err := s.db.QueryRowContext(ctx,
		`SELECT source, repo_key, kind, number, state, title, comments, updated_at
		 FROM items WHERE source = ? AND repo_key = ? AND kind = ? AND number = ?`,
		source, repoKey, kind, number,
	).Scan(&item.Source, &item.RepoKey, &item.Kind, &item.Number, &item.State, &item.Title, &item.Comments, &updated)
	if err == sql.ErrNoRows {
		return Item{}, false, nil
	}
	if err != nil {
		return Item{}, false, fmt.Errorf("get item: %w", err)
	}
	item.UpdatedAt = parseTime(updated)
	return item, true, nil
}

// SaveItem inserts or updates an issue or pull request.
func (s *Store) SaveItem(ctx context.Context, item Item) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO items (source, repo_key, kind, number, state, title, comments, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(source, repo_key, kind, number) DO UPDATE SET
		   state = excluded.state,
		   title = excluded.title,
		   comments = excluded.comments,
		   updated_at = excluded.updated_at`,
		item.Source, item.RepoKey, item.Kind, item.Number, item.State, item.Title, item.Comments,
		item.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("save item: %w", err)
	}
	return nil
}

// GetPipeline returns a previously seen CI run.
func (s *Store) GetPipeline(ctx context.Context, source, repoKey, id string) (Pipeline, bool, error) {
	var item Pipeline
	var updated string
	err := s.db.QueryRowContext(ctx,
		`SELECT source, repo_key, id, name, status, url, ref, sha, updated_at
		 FROM pipelines WHERE source = ? AND repo_key = ? AND id = ?`,
		source, repoKey, id,
	).Scan(&item.Source, &item.RepoKey, &item.ID, &item.Name, &item.Status, &item.URL, &item.Ref, &item.SHA, &updated)
	if err == sql.ErrNoRows {
		return Pipeline{}, false, nil
	}
	if err != nil {
		return Pipeline{}, false, fmt.Errorf("get pipeline: %w", err)
	}
	item.UpdatedAt = parseTime(updated)
	return item, true, nil
}

// SavePipeline inserts or updates a CI run.
func (s *Store) SavePipeline(ctx context.Context, item Pipeline) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO pipelines (source, repo_key, id, name, status, url, ref, sha, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(source, repo_key, id) DO UPDATE SET
		   name = excluded.name,
		   status = excluded.status,
		   url = excluded.url,
		   ref = excluded.ref,
		   sha = excluded.sha,
		   updated_at = excluded.updated_at`,
		item.Source, item.RepoKey, item.ID, item.Name, item.Status, item.URL, item.Ref, item.SHA,
		item.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("save pipeline: %w", err)
	}
	return nil
}

// Mirrors returns all last known push mirror states for a repository.
func (s *Store) Mirrors(ctx context.Context, repoKey string) (map[string]Mirror, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT repo_key, target, address, public_key, last_error, last_updated
		 FROM mirrors WHERE repo_key = ?`, repoKey)
	if err != nil {
		return nil, fmt.Errorf("list mirrors: %w", err)
	}
	defer rows.Close()

	result := map[string]Mirror{}
	for rows.Next() {
		var item Mirror
		var updated string
		if err := rows.Scan(&item.RepoKey, &item.Target, &item.Address, &item.PublicKey, &item.LastError, &updated); err != nil {
			return nil, fmt.Errorf("scan mirror: %w", err)
		}
		item.LastUpdated = parseTime(updated)
		result[item.Target] = item
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mirrors: %w", err)
	}
	return result, nil
}

// SaveMirror stores the last known state of a push mirror.
func (s *Store) SaveMirror(ctx context.Context, item Mirror) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO mirrors (repo_key, target, address, public_key, last_error, last_updated)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(repo_key, target) DO UPDATE SET
		   address = excluded.address,
		   public_key = excluded.public_key,
		   last_error = excluded.last_error,
		   last_updated = excluded.last_updated`,
		item.RepoKey, item.Target, item.Address, item.PublicKey, item.LastError,
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("save mirror: %w", err)
	}
	return nil
}

// IsSeeded reports whether a stream (for example issues or pipelines of a
// repository) has already been scanned once.
func (s *Store) IsSeeded(ctx context.Context, stream string) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM seeds WHERE stream = ?`, stream).Scan(&count); err != nil {
		return false, fmt.Errorf("check seed: %w", err)
	}
	return count > 0, nil
}

// MarkSeeded records that a stream has been scanned once.
func (s *Store) MarkSeeded(ctx context.Context, stream string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO seeds (stream, seeded_at) VALUES (?, ?) ON CONFLICT(stream) DO NOTHING`,
		stream, time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("mark seed: %w", err)
	}
	return nil
}

func parseTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

const managementRepoKeyStore = "__persia_management__"

// RepoKeys returns all known repository keys, excluding the internal
// management room entry.
func (s *Store) RepoKeys(ctx context.Context) ([]string, error) {
	var keys []string
	seen := map[string]bool{}
	for _, query := range []string{
		`SELECT repo_key FROM rooms`,
		`SELECT repo_key FROM mirrors`,
	} {
		rows, err := s.db.QueryContext(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("list repo keys: %w", err)
		}
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan repo key: %w", err)
			}
			if key != managementRepoKeyStore && !seen[key] {
				seen[key] = true
				keys = append(keys, key)
			}
		}
		rows.Close()
	}
	return keys, nil
}

// DeleteRepoState removes all stored state for a repository.
func (s *Store) DeleteRepoState(ctx context.Context, repoKey string) error {
	for _, stmt := range []string{
		`DELETE FROM rooms WHERE repo_key = ?`,
		`DELETE FROM items WHERE repo_key = ?`,
		`DELETE FROM pipelines WHERE repo_key = ?`,
		`DELETE FROM mirrors WHERE repo_key = ?`,
	} {
		if _, err := s.db.ExecContext(ctx, stmt, repoKey); err != nil {
			return fmt.Errorf("delete repo state: %w", err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM seeds WHERE stream LIKE ?`, "%"+repoKey); err != nil {
		return fmt.Errorf("delete repo seeds: %w", err)
	}
	return nil
}

const roomPropsTTL = 5 * time.Minute

type RoomProps struct {
	RoomID    string
	Alias     string
	Public    bool
	UpdatedAt time.Time
}

func (s *Store) RoomProps(ctx context.Context, roomID string) (RoomProps, bool, error) {
	var p RoomProps
	var public int
	var updated string
	err := s.db.QueryRowContext(ctx, `SELECT alias, public, updated_at FROM room_props WHERE room_id = ?`, roomID).Scan(&p.Alias, &public, &updated)
	if err == sql.ErrNoRows {
		return p, false, nil
	}
	if err != nil {
		return p, false, fmt.Errorf("get room props: %w", err)
	}
	p.RoomID = roomID
	p.Public = public != 0
	p.UpdatedAt = parseTime(updated)
	return p, true, nil
}

func (s *Store) SaveRoomProps(ctx context.Context, props RoomProps) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO room_props (room_id, alias, public, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(room_id) DO UPDATE SET alias = excluded.alias, public = excluded.public, updated_at = excluded.updated_at`,
		props.RoomID,
		strings.TrimSpace(props.Alias),
		boolToInt(props.Public),
		props.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("save room props: %w", err)
	}
	return nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
