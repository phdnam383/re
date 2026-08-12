// Package ruleengine runs the operator's RCA rules over a context snapshot
// and returns the root causes they assert, together with the actions they
// recommend.
//
// The pipeline is deliberately small:
//
//	LoadEnabled  →  Facts  →  per-row, per-subject execute  →  merge  →  status
//
// One rca_rule row is one scenario document and may hold several GRL rules.
// The row — not the individual GRL rule — is the unit that is compiled,
// cached, timed out and failed. That boundary is the point of the design: a
// document that produces contradictory or invalid output is discarded whole,
// so a half-applied scenario can never reach a response, while the other rows
// keep their conclusions.
//
// Each row is executed once per subject: once with no subject, then once for
// every VDU and every VNFC the snapshot carries. A rule therefore states what
// it concludes about one entity, and the runner is what applies it to however
// many entities exist. This is what keeps a rule set from naming the instances
// that scaling creates and destroys — an instance-scoped rule goes silent the
// moment its VDU is scaled, and silence from a rule that no longer matches is
// indistinguishable from a healthy system.
//
// A rule that mentions no subject runs on every pass and asserts the same
// claim each time; re-asserting one id with identical metadata is
// corroboration, so it still contributes exactly one root cause.
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
