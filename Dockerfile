FROM golang:1.25-alpine AS builder

WORKDIR /app
COPY ./ .

# Build etl and probe binaries.
RUN set -eu; \
  mkdir -p /app/bin; \
  for d in ./cmd/*; do \
    [ -d "$d" ] || continue; \
    [ -f "$d/main.go" ] || continue; \
    name="$(basename "$d")"; \
    echo "==> building $name"; \
    CGO_ENABLED=0 go build -o "/usr/local/bin/$name" "./cmd/$name"; \
  done

#### development tooling ####
RUN apk add bash jq vim
RUN go test ./internal/...

FROM alpine:3

# Create non-root user/group
RUN addgroup -S app && adduser -S -G app -h /home/app app

WORKDIR /app
COPY --from=builder /usr/local/bin/ /usr/local/bin/

# directories for generated configs and sqlite DB (owned by non-root)
RUN mkdir -p /home/app && \
    chown -R app:app /home/app /app

#### run with non-root user ####
USER app
