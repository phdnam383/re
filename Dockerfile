FROM golang:1.25.7-alpine AS build

WORKDIR /src

COPY . .

ENV CGO_ENABLED=0
ENV GOFLAGS=-mod=vendor
RUN go build -mod=vendor -trimpath -ldflags="-s -w" -o /out/engine ./cmd/engine

FROM alpine:3.20

COPY --from=build /out/engine /engine
COPY --from=build /src/proto /proto

USER 65532:65532
EXPOSE 50053 8080

ENTRYPOINT ["/engine"]
