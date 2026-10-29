package store

import (
	"database/sql"
	"encoding/json"
	"fmt"

	_ "modernc.org/sqlite"
)

// SQLite is the Persister the daemon uses (SPEC §11, Q2).
type SQLite struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS messages (
  id      INTEGER PRIMARY KEY,
  mid     TEXT NOT NULL UNIQUE,
  box     TEXT NOT NULL,
  thread  TEXT NOT NULL,
  read_at INTEGER NOT NULL DEFAULT 0,
  seen_at INTEGER NOT NULL DEFAULT 0,
  body    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS messages_box ON messages(box, id);
CREATE INDEX IF NOT EXISTS messages_thread ON messages(thread);
CREATE TABLE IF NOT EXISTS boxes (
  box     TEXT PRIMARY KEY,
  evicted INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS meta (
  key   TEXT PRIMARY KEY,
  value INTEGER NOT NULL
);
`

// OpenSQLite opens or creates the database file.
func OpenSQLite(path string) (*SQLite, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store schema: %w", err)
	}
	return &SQLite{db: db}, nil
}

func (p *SQLite) Close() error { return p.db.Close() }

func (p *SQLite) LoadAll() ([]Message, map[Address]uint64, uint64, error) {
	rows, err := p.db.Query(`SELECT box, read_at, seen_at, body FROM messages ORDER BY id`)
	if err != nil {
		return nil, nil, 0, err
	}
	defer rows.Close()
	var msgs []Message
	for rows.Next() {
		var box, body string
		var readAt, seenAt int64
		if err := rows.Scan(&box, &readAt, &seenAt, &body); err != nil {
			return nil, nil, 0, err
		}
		var m Message
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			return nil, nil, 0, fmt.Errorf("message %s: %w", box, err)
		}
		m.ReadAt, m.SeenAt = readAt, seenAt
		m.box = Address(box)
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, 0, err
	}
	evicted := map[Address]uint64{}
	brows, err := p.db.Query(`SELECT box, evicted FROM boxes`)
	if err != nil {
		return nil, nil, 0, err
	}
	defer brows.Close()
	for brows.Next() {
		var box string
		var n uint64
		if err := brows.Scan(&box, &n); err != nil {
			return nil, nil, 0, err
		}
		evicted[Address(box)] = n
	}
	var last uint64
	err = p.db.QueryRow(`SELECT value FROM meta WHERE key='last_id'`).Scan(&last)
	if err != nil && err != sql.ErrNoRows {
		return nil, nil, 0, err
	}
	return msgs, evicted, last, nil
}

func (p *SQLite) Insert(box Address, m *Message) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO messages(id, mid, box, thread, read_at, seen_at, body) VALUES(?,?,?,?,?,?,?)`,
		m.ID, m.MID, string(box), m.ThreadID, m.ReadAt, m.SeenAt, string(body)); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES('last_id', ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, m.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (p *SQLite) Mark(mid string, readAt, seenAt int64) error {
	_, err := p.db.Exec(`UPDATE messages SET read_at=?, seen_at=? WHERE mid=?`, readAt, seenAt, mid)
	return err
}

func (p *SQLite) Evict(box Address, mid string, evicted uint64) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM messages WHERE mid=?`, mid); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO boxes(box, evicted) VALUES(?,?) ON CONFLICT(box) DO UPDATE SET evicted=excluded.evicted`, string(box), evicted); err != nil {
		return err
	}
	return tx.Commit()
}
