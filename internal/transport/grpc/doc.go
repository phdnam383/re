// Package grpc adapts the Analysis Service to the generated
// RuleEngine service.
//
// It does three things and nothing else: translate protobuf to domain types
// and back, turn a domain error into a gRPC status, and log the call. There is
// no business logic here, no database access, no retry and no timeout of its
// own — anything this package decided would be a rule the engine applies only
// to gRPC callers, invisible to every other entry point.
//
// The split is what makes that boundary hold: the mapper converts
// representation only, and validation lives in internal/analysis, so a future
// caller arriving by another route is judged by the same rules.
package grpc
