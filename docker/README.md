# Docker Support for Redis Stream Client Go

This directory contains Docker configuration files for development and testing environments.

## Quick Start

### 1. Start the Development Environment

```bash
# Start Redis and development environment
docker-compose up -d redis dev

# Access the development container
docker-compose exec dev bash
```

### 2. Run Examples with Docker

```bash
# Start the complete load balancing demo
docker-compose up redis consumer1 consumer2 consumer3 producer

# Or start individual services
docker-compose up -d redis
docker-compose up consumer1
```

### 3. Run Tests in Docker

```bash
# Run all tests
docker-compose run --rm test

# Run linting
docker-compose run --rm lint

# Run security scanning
docker-compose run --rm security
```

## Services Overview

### Core Services

- **redis**: Redis server with optimized configuration for streams
- **redis-insight**: Web-based Redis monitoring tool (http://localhost:8001)
- **dev**: Development environment with all tools installed

### Example Services

- **consumer1/2/3**: Multiple consumers for load balancing demonstration
- **producer**: Message producer for testing

### Quality Assurance Services

- **test**: Runs the complete test suite
- **lint**: Runs golangci-lint
- **security**: Runs security scanning

## Configuration Files

### `redis.conf`
Optimized Redis configuration for stream workloads:
- Keyspace notifications enabled
- Stream-specific optimizations
- Memory and persistence settings
- Performance tuning

### `docker-compose.yml`
Multi-service setup for:
- Development environment
- Load balancing examples
- Testing and quality assurance

## Development Workflow

### 1. Setup Development Environment

```bash
# Clone the repository
git clone https://github.com/handcoding-labs/redis-stream-client-go.git
cd redis-stream-client-go

# Start development environment
docker-compose up -d redis dev

# Access development container
docker-compose exec dev bash
```

### 2. Development Inside Container

```bash
# Inside the dev container
make deps          # Install dependencies
make test          # Run tests
make lint          # Run linting
make examples      # Run examples
```

### 3. Testing Load Balancing

```bash
# Terminal 1: Start infrastructure
docker-compose up -d redis

# Terminal 2: Start consumers
docker-compose up consumer1 consumer2 consumer3

# Terminal 3: Start producer
docker-compose up producer

# Monitor with Redis Insight
open http://localhost:8001
```

## Docker Commands Reference

### Basic Operations

```bash
# Start all services
docker-compose up

# Start in background
docker-compose up -d

# Stop all services
docker-compose down

# View logs
docker-compose logs -f [service-name]

# Restart a service
docker-compose restart [service-name]
```

### Development Commands

```bash
# Build development image
docker-compose build dev

# Run interactive shell
docker-compose exec dev bash

# Run specific command
docker-compose exec dev go test ./...

# Run one-off container
docker-compose run --rm dev make lint
```

### Cleanup Commands

```bash
# Stop and remove containers
docker-compose down

# Remove volumes (data loss!)
docker-compose down -v

# Remove images
docker-compose down --rmi all

# Full cleanup
docker system prune -a
```

## Environment Variables

### Container Environment Variables

- `POD_NAME`: Unique consumer identifier
- `REDIS_URL`: Redis connection string
- `GO_ENV`: Environment (development/test/production)

### Host Environment Variables

```bash
# Set before running docker-compose
export REDIS_PORT=6379
export REDIS_INSIGHT_PORT=8001
```

## Volumes

### Persistent Volumes

- `redis_data`: Redis data persistence
- `redis_insight_data`: Redis Insight configuration
- `go_mod_cache`: Go module cache for faster builds

### Bind Mounts

- `.:/app`: Source code (development only)

## Networking

### Network Configuration

- Network: `redis-stream-network` (172.20.0.0/16)
- Redis: `redis:6379` (internal)
- Redis Insight: `redis-insight:8001` (internal)

### Port Mapping

- Redis: `localhost:6379`
- Redis Insight: `localhost:8001`

## Production Considerations

### Security

```dockerfile
# Use multi-stage builds
FROM golang:1.23-alpine AS builder
# ... build stage
FROM scratch AS production
# ... minimal production image
```

### Environment Variables

```bash
# Production environment variables
POD_NAME=prod-consumer-${HOSTNAME}
REDIS_URL=redis://prod-redis:6379
REDIS_PASSWORD=your-secure-password
```

### Resource Limits

```yaml
# docker-compose.prod.yml
services:
  consumer:
    deploy:
      resources:
        limits:
          cpus: '0.5'
          memory: 512M
        reservations:
          cpus: '0.25'
          memory: 256M
```

## Troubleshooting

### Common Issues

1. **Port conflicts**
   ```bash
   # Check if ports are in use
   lsof -i :6379
   lsof -i :8001
   ```

2. **Permission issues**
   ```bash
   # Fix volume permissions
   docker-compose exec dev chown -R $(id -u):$(id -g) /app
   ```

3. **Redis connection issues**
   ```bash
   # Check Redis health
   docker-compose exec redis redis-cli ping
   
   # Check Redis configuration
   docker-compose exec redis redis-cli CONFIG GET notify-keyspace-events
   ```

4. **Build issues**
   ```bash
   # Rebuild without cache
   docker-compose build --no-cache dev
   
   # Clear Go module cache
   docker volume rm redis-stream-client-go_go_mod_cache
   ```

### Debugging

```bash
# View container logs
docker-compose logs -f consumer1

# Access container shell
docker-compose exec consumer1 sh

# Check container resources
docker stats

# Inspect network
docker network inspect redis-stream-client-go_redis-stream-network
```

## Monitoring

### Redis Insight

Access Redis Insight at http://localhost:8001 to:
- Monitor stream lengths
- View consumer groups
- Check pending messages
- Monitor keyspace notifications

### Container Monitoring

```bash
# Monitor resource usage
docker stats

# View logs in real-time
docker-compose logs -f --tail=100

# Check health status
docker-compose ps
```
