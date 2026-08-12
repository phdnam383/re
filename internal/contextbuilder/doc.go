// Package contextbuilder turns a request into the ContextSnapshot the Rule
// Engine reasons over: load the enabled context profiles, match their
// selectors against the alerts, merge every match into one deduplicated plan,
// run the VDU, Link and Configuration providers over that plan, and assemble
// the result deterministically.
//
// The package is named contextbuilder rather than context so it never
// collides with the standard library's context, which every file here
// imports.
//
// Nothing about what to fetch is hard-coded. A profile is the only source of
// VDU paths, link subtree pairs and configuration URLs, so one row can be read to
// know what a request will fetch. When no profile matches, the builder
// returns ErrContextProfileNotFound and calls no provider — an engine
// reasoning over a scope nobody declared is worse than one that refuses.
package contextbuilder
