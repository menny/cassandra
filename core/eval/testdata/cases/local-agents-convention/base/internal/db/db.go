package db

import (
	"database/sql"
	"fmt"
)

// SafeQuery is the required wrapper for all DB queries.
func SafeQuery(db *sql.DB, query string, args ...any) (*sql.Rows, error) {
	fmt.Printf("Executing safe query: %s\n", query)
	return db.Query(query, args...)
}
