# Build stage.
#
# The Go version is pinned to the one in go.mod. A toolchain newer than the
# module declares would silently compile with different language semantics than
# the tests ran under.
FROM golang:1.25.7-alpine AS build

WORKDIR /src

# Dependencies first, so an edit to the source does not re-download the module
# graph — which for this module includes the whole of go-git, dragged in by
# grule's resource loader.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO off produces a static binary, which is what lets the runtime image hold
# nothing but the binary itself. Nothing here needs libc: the pgx driver is
# pure Go.
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/engine ./cmd/engine

# Runtime stage.
#
# Distroless static: no shell, no package manager, no libc. The engine opens a
# database connection and a listening socket and does nothing else, so anything
# else in the image is only attack surface.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/engine /engine

# The proto is copied in so an operator can run grpcurl against the pod without
# carrying a copy of the schema — server reflection is deliberately not enabled.
COPY --from=build /src/proto /proto

USER nonroot:nonroot
EXPOSE 30051

ENTRYPOINT ["/engine"]
