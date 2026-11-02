# ============================================================
# Stage 1: Build the gite binary
# ============================================================
FROM golang:1.27-alpine AS builder

WORKDIR /src

# Download dependencies first (cache layer)
COPY go.mod go.sum ./
RUN go mod download

# Copy source code and build
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /bin/gite ./cmd/gite

# ============================================================
# Stage 2: Minimal production image
# ============================================================
FROM alpine:3.20

# Install git (required by gite to run upload-pack / receive-pack)
RUN apk add --no-cache git openssh-client ca-certificates

# Create non-root user to run gite
RUN addgroup -S gite && adduser -S -G gite gite

# Copy compiled binary from builder
COPY --from=builder /bin/gite /usr/local/bin/gite

# gite data directory (repos, db, host key)
ENV GITE_HOME=/data
VOLUME ["/data"]

# SSH git transport port
EXPOSE 2222

# HTTP web + smart HTTP port (Phase 1)
EXPOSE 3000

USER gite

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD nc -z localhost 2222 || exit 1

ENTRYPOINT ["gite"]
CMD ["serve"]
