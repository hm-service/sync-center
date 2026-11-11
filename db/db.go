package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

var ErrConflict = errors.New("parent does not match head")

const keepVersions = 10

const schema = `
CREATE TABLE IF NOT EXISTS namespace (
    id         INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS snapshot (
    id           INTEGER PRIMARY KEY,
    namespace_id INTEGER NOT NULL,
    seq          INTEGER NOT NULL,
    manifest     TEXT    NOT NULL,
    total_bytes  INTEGER NOT NULL,
    created_at   INTEGER NOT NULL,
    UNIQUE (namespace_id, seq)
);
`

type DB struct {
	conn *sql.DB
}

type Namespace struct {
	ID        int64
	Name      string
	CreatedAt int64
	UpdatedAt int64
}

type Snapshot struct {
	Seq        int64
	Manifest   string
	TotalBytes int64
	CreatedAt  int64
}

type SnapshotMeta struct {
	Seq        int64
	CreatedAt  int64
	TotalBytes int64
}

func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	// 单连接：SQLite 写入串行，连接池没有收益，反而会引入锁等待。
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	if _, err := conn.Exec(schema); err != nil {
		conn.Close()
		return nil, err
	}
	return &DB{conn: conn}, nil
}

func (d *DB) Close() error {
	return d.conn.Close()
}

func (d *DB) SyncNamespaces(names []string) error {
	now := time.Now().Unix()
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, name := range names {
		if _, err := tx.Exec(
			"INSERT INTO namespace (name, created_at, updated_at) VALUES (?, ?, ?) ON CONFLICT(name) DO NOTHING",
			name, now, now,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) NamespaceByName(name string) (*Namespace, error) {
	row := d.conn.QueryRow(
		"SELECT id, name, created_at, updated_at FROM namespace WHERE name = ?", name,
	)
	var namespace Namespace
	if err := row.Scan(&namespace.ID, &namespace.Name, &namespace.CreatedAt, &namespace.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &namespace, nil
}

func (d *DB) ListNamespaces() ([]Namespace, error) {
	rows, err := d.conn.Query(
		"SELECT id, name, created_at, updated_at FROM namespace ORDER BY name",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Namespace
	for rows.Next() {
		var namespace Namespace
		if err := rows.Scan(&namespace.ID, &namespace.Name, &namespace.CreatedAt, &namespace.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, namespace)
	}
	return result, rows.Err()
}

func (d *DB) Head(namespaceID int64) (*Snapshot, error) {
	return d.snapshotBy(d.conn.QueryRow(
		"SELECT seq, manifest, total_bytes, created_at FROM snapshot WHERE namespace_id = ? ORDER BY seq DESC LIMIT 1",
		namespaceID,
	))
}

func (d *DB) Snapshot(namespaceID, seq int64) (*Snapshot, error) {
	return d.snapshotBy(d.conn.QueryRow(
		"SELECT seq, manifest, total_bytes, created_at FROM snapshot WHERE namespace_id = ? AND seq = ?",
		namespaceID, seq,
	))
}

func (d *DB) snapshotBy(row *sql.Row) (*Snapshot, error) {
	var snapshot Snapshot
	err := row.Scan(&snapshot.Seq, &snapshot.Manifest, &snapshot.TotalBytes, &snapshot.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func (d *DB) Snapshots(namespaceID int64, limit int) ([]SnapshotMeta, error) {
	rows, err := d.conn.Query(
		"SELECT seq, created_at, total_bytes FROM snapshot WHERE namespace_id = ? ORDER BY seq DESC LIMIT ?",
		namespaceID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SnapshotMeta
	for rows.Next() {
		var meta SnapshotMeta
		if err := rows.Scan(&meta.Seq, &meta.CreatedAt, &meta.TotalBytes); err != nil {
			return nil, err
		}
		result = append(result, meta)
	}
	return result, rows.Err()
}

func (d *DB) AllManifests() ([]string, error) {
	rows, err := d.conn.Query("SELECT manifest FROM snapshot")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var manifest string
		if err := rows.Scan(&manifest); err != nil {
			return nil, err
		}
		result = append(result, manifest)
	}
	return result, rows.Err()
}

// Commit 在一个事务内完成 parent 校验、写入与裁剪。
func (d *DB) Commit(namespaceID int64, parent *int64, manifest string, totalBytes int64) (int64, error) {
	tx, err := d.conn.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var headSeq int64
	var headManifest sql.NullString
	err = tx.QueryRow(
		"SELECT seq, manifest FROM snapshot WHERE namespace_id = ? ORDER BY seq DESC LIMIT 1",
		namespaceID,
	).Scan(&headSeq, &headManifest)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	expected := int64(0)
	if parent != nil {
		expected = *parent
	}
	if expected != headSeq {
		return 0, ErrConflict
	}
	if headManifest.Valid && headManifest.String == manifest {
		return headSeq, nil
	}

	seq := headSeq + 1
	now := time.Now().Unix()
	if _, err := tx.Exec(
		"INSERT INTO snapshot (namespace_id, seq, manifest, total_bytes, created_at) VALUES (?, ?, ?, ?, ?)",
		namespaceID, seq, manifest, totalBytes, now,
	); err != nil {
		return 0, err
	}
	if _, err := tx.Exec("UPDATE namespace SET updated_at = ? WHERE id = ?", now, namespaceID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(
		"DELETE FROM snapshot WHERE namespace_id = ? AND seq <= ?", namespaceID, seq-keepVersions,
	); err != nil {
		return 0, err
	}
	return seq, tx.Commit()
}

// Probe 用临时表验证数据库可读写，供 /readyz 使用。
func (d *DB) Probe() error {
	if _, err := d.conn.Exec("CREATE TABLE IF NOT EXISTS __probe (id INTEGER)"); err != nil {
		return fmt.Errorf("probe create: %w", err)
	}
	if _, err := d.conn.Exec("INSERT INTO __probe (id) VALUES (1)"); err != nil {
		return fmt.Errorf("probe write: %w", err)
	}
	if _, err := d.conn.Exec("DROP TABLE IF EXISTS __probe"); err != nil {
		return fmt.Errorf("probe drop: %w", err)
	}
	return nil
}
