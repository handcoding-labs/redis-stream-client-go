# Multi-stage Dockerfile for Redis Stream Client Go
# Supports both development and production builds

# Build stage
FROM golang:1.23-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download && go mod verify

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags='-w -s -extldflags "-static"' \
    -a -installsuffix cgo \
    -o redis-stream-client \
    ./examples/basic-usage

# Production stage
FROM scratch AS production

# Copy CA certificates for HTTPS requests
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy timezone data
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# Copy the binary
COPY --from=builder /app/redis-stream-client /redis-stream-client

# Set environment variables
ENV POD_NAME=docker-consumer
ENV TZ=UTC

# Expose any ports if needed (none for this library)
# EXPOSE 8080

# Run the application
ENTRYPOINT ["/redis-stream-client"]

# Development stage
FROM golang:1.23-alpine AS development

# Install development tools
RUN apk add --no-cache \
    git \
    ca-certificates \
    tzdata \
    make \
    curl \
    bash \
    redis

# Install development Go tools
RUN go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.54.2 && \
    go install github.com/securecodewarrior/gosec/v2/cmd/gosec@latest && \
    go install golang.org/x/tools/cmd/goimports@latest

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Set development environment variables
ENV GO_ENV=development
ENV POD_NAME=dev-consumer
ENV CGO_ENABLED=0

# Default command for development
CMD ["go", "run", "./examples/basic-usage"]

# Testing stage
FROM development AS testing

# Run tests
RUN make test-coverage

# Linting stage  
FROM development AS linting

# Run linting
RUN make lint

# Security scanning stage
FROM development AS security

# Run security checks
RUN make security
