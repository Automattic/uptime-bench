// Package db manages the MySQL connection and event log writes.
package db

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/go-sql-driver/mysql"
)

// DB wraps a *sql.DB with uptime-bench-specific query methods.
type DB struct {
	db *sql.DB
}

// Open connects to MySQL using the given DSN.
// DSN format: user:password@tcp(host:3306)/uptime_bench?parseTime=true
func Open(dsn string) (*DB, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	return &DB{db: db}, nil
}

// Close releases the database connection pool.
func (d *DB) Close() error {
	return d.db.Close()
}
