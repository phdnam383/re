// Package postgres implements the rule engine's rule repository over
// PostgreSQL.
//
// It depends only on database/sql, so it compiles without a driver; the pgx
// driver is registered in cmd/engine.
package postgres
