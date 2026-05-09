package db

import "fmt"

type DB struct{}

// Execute runs a query. Note: There is NO ExecuteAsync method.
func (d *DB) Execute(query string) error {
	fmt.Println("Executing:", query)
	return nil
}
