# Makefile for Event Store

.PHONY: build run test docker-build docker-push k8s-deploy k8s-delete clean

# Variables
APP_NAME := event-store
DOCKER_IMAGE := $(APP_NAME):latest
NAMESPACE := event-store

# Build the application
build:
	@echo "Building $(APP_NAME)..."
	go build -o $(APP_NAME) ./cmd/main.go

# Run the application locally
run:
	@echo "Running $(APP_NAME)..."
	go run cmd/main.go

# Run tests
test:
	@echo "Running tests..."
	go test -v ./...

# Run tests with coverage
test-coverage:
	@echo "Running tests with coverage..."
	go test -cover -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# Install dependencies
deps:
	@echo "Installing dependencies..."
	go mod download
	go mod tidy

# Build Docker image
docker-build:
	@echo "Building Docker image..."
	docker build -t $(DOCKER_IMAGE) .

# Run locally with Docker
docker-run:
	@echo "Running with Docker..."
	docker run -p 8080:8080 \
		-e NODE_ID=event-store-0 \
		-e HTTP_PORT=8080 \
		-e DATA_DIR=/data/wal \
		-e ETCD_ENDPOINTS=host.docker.internal:2379 \
		$(DOCKER_IMAGE)

# Load image into kind cluster
k8s-load-image:
	@echo "Loading Docker image into kind cluster..."
	@CLUSTER_NAME=$$(kubectl config current-context | sed 's/kind-//'); \
	kind load docker-image $(DOCKER_IMAGE) --name $$CLUSTER_NAME

# Deploy to Kubernetes (builds and loads image first)
k8s-deploy: docker-build k8s-load-image
	@echo "Deploying to Kubernetes..."
	kubectl apply -f deploy/deployment.yaml
	@echo "Waiting for pods to be ready..."
	kubectl wait --for=condition=ready pod -l app=event-store -n event-store --timeout=300s || true

# Delete from Kubernetes
k8s-delete:
	@echo "Deleting from Kubernetes..."
	kubectl delete -f deploy/deployment.yaml

# View logs (requires pod name)
k8s-logs:
	@echo "Viewing logs..."
	kubectl logs -n $(NAMESPACE) -f event-store-0

# Port forward to local machine
k8s-port-forward:
	@echo "Port forwarding..."
	kubectl port-forward -n $(NAMESPACE) svc/event-store-external 8080:8080

# Check pod status
k8s-status:
	@echo "Checking pod status..."
	kubectl get pods -n $(NAMESPACE)

# Test the deployed application
k8s-test:
	@echo "Testing deployed application..."
	@echo "Checking health..."
	kubectl run test-pod --rm -i --restart=Never --image=curlimages/curl:latest -n $(NAMESPACE) --timeout=10s -- \
		curl -m 5 -s http://event-store-external:8080/health
	@echo "\nWriting test event..."
	kubectl run test-pod --rm -i --restart=Never --image=curlimages/curl:latest -n $(NAMESPACE) --timeout=10s -- \
		curl -m 5 -s -X POST http://event-store-external:8080/events \
		-H "Content-Type: application/json" \
		-d '{"type":"TestEvent","aggregate_id":"test-123","payload":{"message":"hello"}}'
	@echo "\nReading test event..."
	kubectl run test-pod --rm -i --restart=Never --image=curlimages/curl:latest -n $(NAMESPACE) --timeout=10s -- \
		curl -m 5 -s http://event-store-external:8080/events/test-123

# Validate leader election and replication
k8s-validate:
	@echo "=== Validating Leader Election and Replication ==="
	@echo "\n1. Checking all pods are running..."
	@kubectl get pods -n $(NAMESPACE) | grep event-store
	@echo "\n2. Checking which node is the leader..."
	@kubectl run test-leader --rm -i --restart=Never --image=curlimages/curl:latest -n $(NAMESPACE) --timeout=10s -- \
		curl -m 5 -s http://event-store-external:8080/status | grep -o '"id":"[^"]*","is_leader":[^,]*' || echo "Could not determine leader"
	@echo "\n3. Writing test event..."
	@kubectl run test-write --rm -i --restart=Never --image=curlimages/curl:latest -n $(NAMESPACE) --timeout=10s -- \
		curl -m 5 -s -X POST http://event-store-external:8080/events \
		-H "Content-Type: application/json" \
		-d '{"type":"ReplicationTest","aggregate_id":"repl-test-$$","payload":{"test":"replication"}}'
	@echo "\n4. Checking WAL files on all nodes..."
	@for i in 0 1 2; do \
		echo "WAL files on event-store-$$i:"; \
		kubectl exec -n $(NAMESPACE) event-store-$$i -- ls -lh /data/wal/ 2>/dev/null || echo "  No WAL files yet"; \
	done
	@echo "\n5. Recent logs from leader..."
	@kubectl logs -n $(NAMESPACE) -l app=event-store --tail=5 | grep -E "(leader|follower|replicat)" | head -10

# Detailed replication test - create event and verify on all nodes
k8s-test-replication:
	@echo "=== Testing Event Replication Across All Nodes ==="
	@echo "\n📋 Step 1: Checking cluster status..."
	@kubectl get pods -n $(NAMESPACE) -l app=event-store --no-headers | awk '{print "  ✓", $$1, "-", $$3}'
	
	@echo "\n📋 Step 2: Identifying the leader..."
	@kubectl run find-leader --rm -i --restart=Never --image=curlimages/curl:latest -n $(NAMESPACE) --timeout=15s -- \
		sh -c 'for i in 0 1 2; do echo "Checking event-store-$$i..."; curl -s http://event-store-$$i.event-store:8080/status | grep -o "\"is_leader\":[^,]*" && echo " (event-store-$$i)"; done' 2>/dev/null || true
	
	@echo "\n📋 Step 3: Writing test event to the cluster..."
	@TIMESTAMP=$$(date +%s); \
	kubectl run test-write-$$TIMESTAMP --rm -i --restart=Never --image=curlimages/curl:latest -n $(NAMESPACE) --timeout=15s -- \
		curl -s -X POST http://event-store-external:8080/events \
		-H "Content-Type: application/json" \
		-d "{\"type\":\"ReplicationTest\",\"aggregate_id\":\"repl-test-$$TIMESTAMP\",\"payload\":{\"message\":\"Testing replication at $$TIMESTAMP\",\"timestamp\":$$TIMESTAMP}}"
	
	@echo "\n📋 Step 4: Waiting for replication (2 seconds)..."
	@sleep 2
	
	@echo "\n📋 Step 5: Verifying WAL files exist on all nodes..."
	@for i in 0 1 2; do \
		echo "\n  Node event-store-$$i:"; \
		kubectl exec -n $(NAMESPACE) event-store-$$i -- ls -lh /data/wal/ 2>/dev/null | tail -n +2 || echo "    ⚠️  No WAL files"; \
	done
	
	@echo "\n📋 Step 6: Checking event count on each node..."
	@TIMESTAMP=$$(date +%s); \
	for i in 0 1 2; do \
		echo "\n  Querying event-store-$$i:"; \
		kubectl run query-node-$$i-$$TIMESTAMP --rm -i --restart=Never --image=curlimages/curl:latest -n $(NAMESPACE) --timeout=15s -- \
			curl -s http://event-store-$$i.event-store:8080/events 2>/dev/null | grep -o '"count":[0-9]*' || echo "    Failed to query"; \
		sleep 1; \
	done
	
	@echo "\n\n✅ Replication test complete!"
	@echo "💡 All nodes should have the same WAL files and event count."

# Clean build artifacts
clean:
	@echo "Cleaning..."
	rm -f $(APP_NAME)
	rm -f coverage.out coverage.html
	rm -rf data/

# Format code
fmt:
	@echo "Formatting code..."
	go fmt ./...

# Lint code
lint:
	@echo "Linting code..."
	golangci-lint run

# Run etcd locally with Docker
etcd-local:
	@echo "Starting etcd locally..."
	docker run -d --name etcd \
		-p 2379:2379 \
		-p 2380:2380 \
		quay.io/coreos/etcd:v3.5.11 \
		/usr/local/bin/etcd \
		--listen-client-urls http://0.0.0.0:2379 \
		--advertise-client-urls http://localhost:2379

# Stop etcd
etcd-stop:
	@echo "Stopping etcd..."
	docker stop etcd
	docker rm etcd

# Full local setup
local-setup: etcd-local
	@echo "Waiting for etcd to start..."
	@sleep 3
	@echo "Local setup complete. Run 'make run' to start the application."

# Full cleanup
full-clean: clean etcd-stop
	@echo "Full cleanup complete."

# Docker Compose - Run all instances locally
compose-up: docker-build
	@echo "Starting all services with Docker Compose..."
	docker-compose up -d
	@echo "Waiting for services to be healthy..."
	@sleep 5
	@docker-compose ps

# Docker Compose - View logs from all instances
compose-logs:
	@echo "Viewing logs from all services..."
	docker-compose logs -f

# Docker Compose - View logs from specific instance
compose-logs-0:
	docker-compose logs -f event-store-0

compose-logs-1:
	docker-compose logs -f event-store-1

compose-logs-2:
	docker-compose logs -f event-store-2

# Docker Compose - Check status
compose-status:
	@echo "Checking service status..."
	docker-compose ps

# Docker Compose - Stop all instances
compose-down:
	@echo "Stopping all services..."
	docker-compose down

# Docker Compose - Full cleanup (including volumes)
compose-clean:
	@echo "Stopping and removing all services and volumes..."
	docker-compose down -v
	@echo "Docker Compose cleanup complete."

# Docker Compose - Restart all instances
compose-restart:
	@echo "Restarting all services..."
	docker-compose restart

# Docker Compose - Test the cluster
compose-test:
	@echo "Testing Docker Compose cluster..."
	@echo "\n1. Checking health of all instances..."
	@curl -s http://localhost:8080/health && echo " ✓ event-store-0 healthy"
	@curl -s http://localhost:8081/health && echo " ✓ event-store-1 healthy"
	@curl -s http://localhost:8082/health && echo " ✓ event-store-2 healthy"
	@echo "\n2. Checking status to find leader..."
	@echo "Event Store 0:"; curl -s http://localhost:8080/status | grep -o '"is_leader":[^,]*'
	@echo "Event Store 1:"; curl -s http://localhost:8081/status | grep -o '"is_leader":[^,]*'
	@echo "Event Store 2:"; curl -s http://localhost:8082/status | grep -o '"is_leader":[^,]*'
	@echo "\n3. Writing test event to leader..."
	@curl -s -X POST http://localhost:8080/events \
		-H "Content-Type: application/json" \
		-d '{"type":"ComposeTest","aggregate_id":"compose-test-123","payload":{"message":"testing compose cluster"}}'
	@echo "\n4. Reading event from all nodes..."
	@echo "From node 0:"; curl -s http://localhost:8080/events/compose-test-123
	@echo "From node 1:"; curl -s http://localhost:8081/events/compose-test-123
	@echo "From node 2:"; curl -s http://localhost:8082/events/compose-test-123
