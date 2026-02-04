# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /workspace

# Install ca-certificates for HTTPS calls to AWS
RUN apk add --no-cache ca-certificates

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary (supports multi-arch via docker buildx)
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -ldflags="-w -s" -o controller ./cmd/controller

# Runtime stage
FROM alpine:3.19

WORKDIR /

# Install ca-certificates for AWS API calls
RUN apk add --no-cache ca-certificates tzdata

# Copy binary from builder
COPY --from=builder /workspace/controller /controller

# Run as non-root user
RUN adduser -D -u 65532 controller
USER 65532

ENTRYPOINT ["/controller"]
