// Package postgres implements the profile repository and the VDU and Link
// providers over PostgreSQL.
//
// It depends only on database/sql, so it compiles without a driver; the pgx
// driver is registered in cmd/engine. Queries build IN-lists with numbered
// placeholders rather than driver-specific array types to keep that true.
package postgres
