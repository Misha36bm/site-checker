# Build from the repository root: docker build -f docker/api/Dockerfile .
FROM golang:1.26-alpine AS build
WORKDIR /build
COPY src/go.mod src/go.sum ./
RUN go mod download
COPY src/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM alpine:3.22
# ca-certificates: verifying TLS of checked sites. busybox wget: the Compose healthcheck.
# The directory is created here so a new named volume inherits its ownership.
RUN apk add --no-cache ca-certificates \
    && adduser -D -H -u 10001 app \
    && mkdir -p /app/storage/screenshots \
    && chown -R app:app /app/storage
COPY --from=build /out/server /app/server
USER app
EXPOSE 8080
ENTRYPOINT ["/app/server"]
