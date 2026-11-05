package store

import "github.com/ncruces/go-sqlite3"

// readerAuthorizer is the deny-by-default policy of the web role. It was verified with
// docs/spikes/sqlite-hardening.go.txt: without it ATTACH DATABASE succeeds even on a
// read-only connection.
//
// Allowed: SELECT; reading the application tables and the FTS5 shadow tables; a short list
// of harmless functions; reading PRAGMA query_only and data_version (the driver and FTS5
// use them internally). Everything else is denied: ATTACH, any write, DDL, other PRAGMAs,
// sqlite_master, unknown functions.
func readerAuthorizer(action sqlite3.AuthorizerActionCode, name3rd, name4th, _, _ string) sqlite3.AuthorizerReturnCode {
	switch action {
	case sqlite3.AUTH_SELECT:
		return sqlite3.AUTH_OK
	case sqlite3.AUTH_READ:
		if readableTable(name3rd) {
			return sqlite3.AUTH_OK
		}
	case sqlite3.AUTH_FUNCTION:
		switch name4th {
		case "match", "count", "min", "max", "coalesce", "length", "lower", "upper":
			return sqlite3.AUTH_OK
		}
	case sqlite3.AUTH_PRAGMA:
		if name3rd == "query_only" && (name4th == "" || name4th == "1") {
			return sqlite3.AUTH_OK
		}
		if (name3rd == "data_version" || name3rd == "user_version") && name4th == "" {
			return sqlite3.AUTH_OK // read only: the value is not assignable under query_only
		}
	}
	return sqlite3.AUTH_DENY
}

func readableTable(name string) bool {
	switch name {
	case "programs", "programs_fts":
		return true
	case "programs_fts_data", "programs_fts_idx", "programs_fts_docsize", "programs_fts_config", "programs_fts_content":
		return true
	}
	return false
}
