package grpc

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"

	"re/gen/mdafv1"
)

// LoggingInterceptor returns a unary interceptor that records one line per
// call.
//
// What it logs is a deliberately short list: the method, the caller's request
// id, how long it took, the gRPC code it ended with, and — on success — the
// three statuses. That is enough to answer "is the engine healthy, and which
// request was this" without the log becoming a copy of the traffic.
//
// What it does not log is the point. Alert additional_information, context
// values, rule content and action values all describe the operator's network
// and its configuration; a request log that carried them would quietly turn
// every log sink into a place that data has to be protected.
//
// It changes neither the response nor the error, and it does not recover from
// panics: swallowing one here would turn a crash into a hung request and hide
// the stack that explains it. Panic recovery, tracing and metrics are separate
// concerns and belong to separate interceptors.
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
			// The outcome only. By the time an error reaches an interceptor
			// the handler has already converted it to a gRPC status, so the
			// detail here would be the same sanitised message the caller got —
			// "code" already says that. The cause is logged by the server
			// adapter, which is the last place that still holds the original
			// error.
			logger.ErrorContext(ctx, "rpc failed", attrs...)
			return resp, err
		}

		if r, ok := resp.(*mdafv1.AnalyzeAlertResponse); ok {
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

// requestIDOf extracts the caller's correlation id, or "" for a request the
// interceptor does not recognise. The id is the caller's own string and is
// logged as-is; it is the only part of the request that appears in the log.
func requestIDOf(req any) string {
	if r, ok := req.(*mdafv1.AnalyzeAlertRequest); ok {
		return r.GetRequestId()
	}
	return ""
}
