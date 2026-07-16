# Stage 1: Build the Go binary
FROM golang:1.23-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git

WORKDIR /build

# Copy dependency files and download packages
COPY go.mod go.sum ./
RUN go mod download

# Copy source files
COPY . .

# Build the binary with size optimizations (strip symbols)
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o catena .

# Stage 2: Create the minimal execution image
FROM alpine:latest

RUN apk add --no-cache tzdata ca-certificates

WORKDIR /app

# Copy the binary from the build stage
COPY --from=builder /build/catena /app/catena

# Create directory to store persistent SQLite database files
RUN mkdir -p /app/data

# Expose HTTP port
EXPOSE 8080

# Run serve by default binding to all interfaces and pointing to /app/data volume
ENTRYPOINT ["/app/catena", "serve", "--db", "/app/data/catena.db", "--host", "0.0.0.0", "--port", "8080"]
