package grpc

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"re/internal/analysis"
	"re/internal/contextbuilder"
	"re/internal/ruleengine"
)

// internalMessage is what a caller is told when something inside the engine
// broke.
//
// Deliberately uninformative. An internal failure's text is assembled from
// whatever went wrong — a DSN in a connection error, a SQL fragment, a
// configuration URL with credentials in it, a fragment of rule content — and
// none of that belongs on a wire the caller does not own. The full error is
// logged server-side, where an operator can read it.
const internalMessage = "internal error"

// toStatusError maps a domain error to the gRPC status a caller acts on.
//
// Matching is by errors.Is throughout, because the Analysis Service wraps each
// stage's failure with its own context. Comparing message text instead would
// break the first time a wrapper's wording changed.
func toStatusError(err error) error {
	if err == nil {
		return nil
	}

	switch {
	// Cancellation first: a deadline that expired inside a query surfaces as a
	// driver error wrapping context.DeadlineExceeded, and reporting that as
	// Internal would send an operator hunting a bug that is really a caller
	// who stopped waiting.
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, context.Canceled.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, context.DeadlineExceeded.Error())

	case errors.Is(err, analysis.ErrInvalidRequest):
		// The caller can fix this, so it carries the field that was wrong.
		return status.Error(codes.InvalidArgument, err.Error())

	// Both definition gaps are FailedPrecondition rather than NotFound: the
	// engine is configured wrongly, not asked about something absent. The
	// messages are the sentinels' own text, kept stable so a caller can tell
	// which half of the configuration is missing without parsing a wrapper.
	case errors.Is(err, contextbuilder.ErrContextProfileNotFound):
		return status.Error(codes.FailedPrecondition, contextbuilder.ErrContextProfileNotFound.Error())
	case errors.Is(err, ruleengine.ErrRCARuleNotFound):
		return status.Error(codes.FailedPrecondition, ruleengine.ErrRCARuleNotFound.Error())

	default:
		return status.Error(codes.Internal, internalMessage)
	}
}
