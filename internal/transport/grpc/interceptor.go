package grpc

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	"re/gen/mdafv1"
)

func LoggingInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		elapsed := time.Since(start)

		attrs := []any{
			"method", info.FullMethod,
			"request_id", requestIDOf(req),
			"duration_ms", elapsed.Milliseconds(),
			"code", status.Code(err).String(),
		}

		if err != nil {

			logger.ErrorContext(ctx, "rpc failed", attrs...)
			return resp, err
		}

		if r, ok := resp.(*mdafv1.AlertRuleAnalysisResponse); ok {
			attrs = append(attrs,
				"overall_status", r.GetStatus().GetOverall(),
				"context_status", r.GetStatus().GetContext(),
				"rca_status", r.GetStatus().GetRca(),
				"root_causes", len(r.GetRca().GetRootCauses()),
				"missing_context", len(r.GetMeta().GetMissingContext()),
			)
		}
		logger.InfoContext(ctx, "rpc completed", attrs...)
		return resp, err
	}
}

func requestIDOf(req any) string {
	if r, ok := req.(*mdafv1.AlertRuleAnalysisRequest); ok {
		return r.GetRequestId()
	}
	return ""
}
