// Package ruleengine runs the operator's RCA rules over a context snapshot
// and returns the root causes they assert, together with the actions they
// recommend.
//
// The pipeline is deliberately small:
//
//	LoadEnabled  →  Facts  →  per-row execute  →  merge  →  status
//
// One rca_rule row is one scenario document and may hold several GRL rules.
// The row — not the individual GRL rule — is the unit that is compiled,
// cached, timed out and failed. That boundary is the point of the design: a
// document that produces contradictory or invalid output is discarded whole,
// so a half-applied scenario can never reach a response, while the other rows
// keep their conclusions.
//
// Rules see the snapshot only through Facts, which is read-only and holds no
// database handle and no HTTP client. Rule content is data loaded from
// PostgreSQL; letting it reach outside the snapshot would turn a rule edit
// into arbitrary I/O.
//
// The dependency rule matches the rest of the engine: this package imports
// internal/analysis for the domain types and is imported by cmd/engine, never
// the other way round.
package ruleengine
