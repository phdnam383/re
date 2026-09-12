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

const internalMessage = "internal error"

func toStatusError(err error) error {
	if err == nil {
		return nil
	}

	switch {

	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, context.Canceled.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, context.DeadlineExceeded.Error())

	case errors.Is(err, analysis.ErrInvalidRequest):

		return status.Error(codes.InvalidArgument, err.Error())

	case errors.Is(err, contextbuilder.ErrContextProfileNotFound):
		return status.Error(codes.FailedPrecondition, contextbuilder.ErrContextProfileNotFound.Error())
	case errors.Is(err, ruleengine.ErrRCARuleNotFound):
		return status.Error(codes.FailedPrecondition, ruleengine.ErrRCARuleNotFound.Error())

	default:
		return status.Error(codes.Internal, internalMessage)
	}
}
