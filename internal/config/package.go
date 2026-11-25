// Package config holds the TOML configuration schema, loading, validation,
// comment-preserving edits and file watching (design §28).
//
// The schema (see Schema) is the single source of truth. Load turns a file
// into a *Config of typed values; Doc edits a file in place without
// disturbing comments, ordering or formatting; Watch calls back when the
// file changes.
package config
