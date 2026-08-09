// Package configuration implements the Configuration Provider: one plain
// HTTP GET per {path, key, url} target named by a profile.
//
// It reads the *effective* configuration an NF is running. It must never fall
// back to the declared values in PostgreSQL — a failed read becomes a
// MissingContext, because a declared value standing in for an effective one
// would let a rule conclude from configuration that is not actually applied.
package configuration
