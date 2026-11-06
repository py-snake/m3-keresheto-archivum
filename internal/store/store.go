// Package store is the SQLite layer.
//
// Two roles use it, with very different powers:
//
//   - the reader (m3 serve) opens the published file read-only and immutable, behind a
//     deny-by-default SQLite authorizer: it can run SELECTs on the application tables, nothing else;
//   - the writer (m3 ingest) works on a private copy of the database and publishes it with an
//     atomic rename (see Publisher). A published file is never modified in place.
package store

import (
	"database/sql"
	_ "embed"
	"fmt"
	"net/url"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/fts5"
)

//go:embed schema.sql
var schemaSQL string

// SchemaVersion is stored in PRAGMA user_version.
const SchemaVersion = 3

// SchemaSQL returns the SQL that creates an empty database of the current schema version.
func SchemaSQL() string { return schemaSQL }

// DBFile is the name of the published database inside the data directory.
const DBFile = "m3.sqlite"

func fileDSN(path string, q url.Values) string {
	return (&url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}).String()
}

// openWriter opens a database for reading and writing. Rollback journal (no WAL): the
// file is complete and self-contained after every commit, and no -wal/-shm files appear.
func openWriter(path string) (*sql.DB, error) {
	db, err := driver.Open(fileDSN(path, nil), func(c *sqlite3.Conn) error {
		if err := fts5.Register(c); err != nil {
			return err
		}
		for _, p := range []string{
			"PRAGMA journal_mode = DELETE",
			"PRAGMA synchronous = FULL",
			"PRAGMA busy_timeout = 5000",
			"PRAGMA trusted_schema = OFF",
			"PRAGMA cell_size_check = ON",
		} {
			if err := c.Exec(p); err != nil {
				return fmt.Errorf("%s: %w", p, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer, and FTS5 is registered per connection
	return db, nil
}
