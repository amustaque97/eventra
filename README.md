# Distributed WAL-Based Event Store

A lightweight, distributed event store implementation using Write-Ahead Logging (WAL), event sourcing, and leader-follower replication with Kubernetes orchestration.

## Features

- **Write-Ahead Logging (WAL)**: Durable event storage with fsync guarantees
- **Event Sourcing**: Events as the single source of truth
- **Leader-Based Replication**: Simplified consensus using etcd for leader election
- **HTTP API**: RESTful endpoints for event operations (no gRPC dependency)
- **Kubernetes Native**: Designed to run on StatefulSets with persistent volumes
- **Fault Tolerant**: Automatic leader failover and recovery

## Architecture

### Components

- **Event Store**: In-memory state machine with WAL backing
- **WAL Engine**: Segment-based append-only log with CRC32 checksums
- **Replicator**: Handles event replication from leader to followers
- **Leader Election**: etcd-based consensus for leader selection
- **HTTP Server**: REST API for client interactions

### Deployment

The system runs as a Kubernetes StatefulSet with:
- 3 event store replicas
- 3 etcd replicas for coordination
- Persistent volumes for WAL storage

## Quick Start

### Prerequisites

- Go 1.23+
- Docker
- Kubernetes cluster (or kind for local testing)

### Build

```bash
# Install dependencies
go mod download

# Build binary
go build -o event-store ./cmd/main.go

# Build Docker image
docker build -t event-store:latest .
```

### Deploy to Kubernetes

```bash
# Apply all manifests
kubectl apply -f deploy/deployment.yaml

# Check pod status
kubectl get pods -n event-store

# Check logs
kubectl logs -n event-store event-store-0 -f
```

### Local Development

```bash
# Set environment variables
export NODE_ID=event-store-0
export HTTP_PORT=8080
export DATA_DIR=./data/wal
export ETCD_ENDPOINTS=localhost:2379

# Run etcd locally
docker run -d --name etcd \
  -p 2379:2379 \
  -p 2380:2380 \
  quay.io/coreos/etcd:v3.5.11 \
  /usr/local/bin/etcd \
  --listen-client-urls http://0.0.0.0:2379 \
  --advertise-client-urls http://localhost:2379

# Run the application
go run cmd/main.go
```

## API Usage

### Write an Event

```bash
curl -X POST http://localhost:8080/events \
  -H "Content-Type: application/json" \
  -d '{
    "type": "UserCreated",
    "aggregate_id": "user-123",
    "payload": {
      "name": "John Doe",
      "email": "john@example.com"
    },
    "metadata": {
      "source": "api"
    }
  }'
```

Response:
```json
{
  "success": true,
  "event_id": "550e8400-e29b-41d4-a716-446655440000",
  "index": 1
}
```

### Read Events by Aggregate

```bash
curl http://localhost:8080/events/user-123
```

Response:
```json
{
  "events": [
    {
      "id": "550e8400-e29b-41d4-a716-446655440000",
      "type": "UserCreated",
      "timestamp": 1699284000,
      "aggregate_id": "user-123",
      "payload": {
        "name": "John Doe",
        "email": "john@example.com"
      }
    }
  ],
  "count": 1
}
```

### Check Node Status

```bash
curl http://localhost:8080/status
```

Response:
```json
{
  "id": "event-store-0",
  "is_leader": true,
  "leader_id": "event-store-0",
  "term": 5,
  "commit_index": 100,
  "last_applied": 100
}
```

### Health Check

```bash
curl http://localhost:8080/health
```

Response:
```json
{
  "status": "healthy",
  "node_id": "event-store-0",
  "is_leader": true,
  "leader_id": "event-store-0",
  "timestamp": "2025-11-06T10:00:00Z"
}
```

## Configuration

Environment variables:

- `NODE_ID`: Unique identifier for this node (default: event-store-0)
- `HTTP_PORT`: HTTP server port (default: 8080)
- `DATA_DIR`: Directory for WAL files (default: /data/wal)
- `ETCD_ENDPOINTS`: Comma-separated etcd endpoints (default: localhost:2379)
- `SERVICE_NAME`: Kubernetes service name (default: event-store)
- `NAMESPACE`: Kubernetes namespace (default: default)

## Project Structure

```
.
├── cmd/
│   └── main.go                 # Application entry point
├── internal/
│   ├── config/
│   │   └── config.go           # Configuration management
│   ├── leader/
│   │   └── election.go         # Leader election with etcd
│   ├── models/
│   │   └── event.go            # Data models and types
│   ├── replication/
│   │   └── replicator.go       # Replication logic
│   ├── server/
│   │   └── http_server.go      # HTTP API handlers
│   ├── store/
│   │   └── store.go            # Event store and state machine
│   └── wal/
│       └── wal.go              # Write-Ahead Log implementation
├── deploy/
│   └── deployment.yaml         # Kubernetes manifests
├── Dockerfile                  # Container image
├── go.mod                      # Go dependencies
└── event_store_design.md       # System design document
```

## Design Principles

1. **Durability**: Every write is fsync'd to disk before acknowledgment
2. **Consistency**: All replicas apply events in the same order
3. **Availability**: System continues operating with majority of nodes
4. **Simplicity**: Simplified leader-follower model (not full Raft)

## Fault Tolerance

- **Leader Failure**: Automatic re-election within 5-7 seconds
- **Follower Failure**: Leader continues with remaining quorum
- **Network Partition**: Minority partition stops accepting writes
- **Data Loss**: Only uncommitted entries in fsync window may be lost

## Monitoring

Key metrics exposed:

- WAL entries written
- WAL size and fsync duration
- Replication lag per follower
- Leader election events
- Applied entries and commit index

## Testing

```bash
# Run unit tests
go test ./...

# Run with coverage
go test -cover ./...

# Run specific package
go test ./internal/wal/
```

## License

MIT

## Contributing

Contributions are welcome! Please open an issue or submit a pull request.
