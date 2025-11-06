# Example API Calls

This document provides example API calls for testing the Event Store.

## Prerequisites

Ensure the event store is running and accessible at `http://localhost:8080`.

## 1. Health Check

```bash
curl http://localhost:8080/health
```

**Expected Response:**
```json
{
  "status": "healthy",
  "node_id": "event-store-0",
  "is_leader": true,
  "leader_id": "event-store-0",
  "timestamp": "2025-11-06T10:00:00Z"
}
```

## 2. Node Status

```bash
curl http://localhost:8080/status
```

**Expected Response:**
```json
{
  "id": "event-store-0",
  "is_leader": true,
  "leader_id": "event-store-0",
  "term": 1,
  "commit_index": 0,
  "last_applied": 0,
  "last_heartbeat": "2025-11-06T10:00:00Z"
}
```

## 3. Write Events

### Create User Event

```bash
curl -X POST http://localhost:8080/events \
  -H "Content-Type: application/json" \
  -d '{
    "type": "UserCreated",
    "aggregate_id": "user-123",
    "payload": {
      "name": "John Doe",
      "email": "john@example.com",
      "age": 30
    },
    "metadata": {
      "source": "api",
      "version": "1.0"
    }
  }'
```

### Update User Event

```bash
curl -X POST http://localhost:8080/events \
  -H "Content-Type: application/json" \
  -d '{
    "type": "UserUpdated",
    "aggregate_id": "user-123",
    "payload": {
      "email": "john.doe@example.com"
    },
    "metadata": {
      "source": "api"
    }
  }'
```

### Order Placed Event

```bash
curl -X POST http://localhost:8080/events \
  -H "Content-Type: application/json" \
  -d '{
    "type": "OrderPlaced",
    "aggregate_id": "order-456",
    "payload": {
      "user_id": "user-123",
      "items": [
        {"product_id": "prod-1", "quantity": 2},
        {"product_id": "prod-2", "quantity": 1}
      ],
      "total": 99.99
    }
  }'
```

## 4. Read Events

### Read All Events

```bash
curl http://localhost:8080/events
```

### Read Events for Specific Aggregate

```bash
curl http://localhost:8080/events/user-123
```

**Expected Response:**
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
        "email": "john@example.com",
        "age": 30
      },
      "metadata": {
        "source": "api",
        "version": "1.0"
      }
    },
    {
      "id": "550e8400-e29b-41d4-a716-446655440001",
      "type": "UserUpdated",
      "timestamp": 1699284100,
      "aggregate_id": "user-123",
      "payload": {
        "email": "john.doe@example.com"
      },
      "metadata": {
        "source": "api"
      }
    }
  ],
  "count": 2
}
```

## 5. Testing Replication

When writing to a follower node, you'll get a redirect response:

```bash
# Try writing to a follower (assuming event-store-1 is a follower)
curl -X POST http://event-store-1.event-store.event-store.svc.cluster.local:8080/events \
  -H "Content-Type: application/json" \
  -d '{
    "type": "TestEvent",
    "aggregate_id": "test-1",
    "payload": {"message": "hello"}
  }'
```

**Expected Response (307 Redirect):**
```json
{
  "message": "not the leader, redirect to leader",
  "leader_id": "event-store-0",
  "leader_address": "event-store-0.event-store.event-store.svc.cluster.local:8080"
}
```

## 6. Load Testing Script

```bash
#!/bin/bash

# Load test script - creates 100 events
for i in {1..100}; do
  curl -X POST http://localhost:8080/events \
    -H "Content-Type: application/json" \
    -d "{
      \"type\": \"LoadTest\",
      \"aggregate_id\": \"load-test-$i\",
      \"payload\": {
        \"iteration\": $i,
        \"timestamp\": $(date +%s)
      }
    }" \
    -s -o /dev/null -w "Request $i: %{http_code}\n"
  
  # Small delay between requests
  sleep 0.1
done
```

Save this as `load-test.sh`, make it executable (`chmod +x load-test.sh`), and run it.

## 7. Monitoring

### Check WAL Directory

If running locally:
```bash
ls -lh ./data/wal/
```

In Kubernetes:
```bash
kubectl exec -n event-store event-store-0 -- ls -lh /data/wal/
```

### Watch Events in Real-time

```bash
# Terminal 1: Watch status
watch -n 1 'curl -s http://localhost:8080/status | jq'

# Terminal 2: Create events
./load-test.sh
```

## 8. Failure Testing

### Test Leader Failover

```bash
# Delete the leader pod
kubectl delete pod -n event-store event-store-0

# Watch status to see new leader election
kubectl get pods -n event-store -w

# Verify new leader
curl http://localhost:30080/status
```

### Test Follower Recovery

```bash
# Delete a follower
kubectl delete pod -n event-store event-store-1

# Write some events
curl -X POST http://localhost:30080/events \
  -H "Content-Type: application/json" \
  -d '{"type": "Test", "aggregate_id": "test-recovery", "payload": {}}'

# Wait for pod to restart
kubectl wait --for=condition=ready pod/event-store-1 -n event-store

# Check that events were replicated
kubectl exec -n event-store event-store-1 -- ls -lh /data/wal/
```

## 9. Complete Kubernetes Replication Test Guide

### Step-by-Step: Deploy and Verify Replication

#### 1. Deploy to Kubernetes
```bash
# Build, load image, and deploy all components
make k8s-deploy

# Verify all pods are running
make k8s-status
```

Expected output:
```
NAME             READY   STATUS    RESTARTS   AGE
event-store-0    1/1     Running   0          1m
event-store-1    1/1     Running   0          1m
event-store-2    1/1     Running   0          1m
etcd-0           1/1     Running   0          1m
etcd-1           1/1     Running   0          1m
etcd-2           1/1     Running   0          1m
```

#### 2. Port Forward (in separate terminal)
```bash
# Forward port to access from localhost
make k8s-port-forward

# Keep this terminal open!
```

#### 3. Run Automated Replication Test
```bash
# Comprehensive replication test
make k8s-test-replication
```

This will:
- ✅ Check all pods are running
- ✅ Identify which node is the leader
- ✅ Write a test event to the cluster
- ✅ Verify WAL files exist on all 3 nodes
- ✅ Check event count on each node

#### 4. Manual Testing - Create Event and Verify

**4a. Create an event:**
```bash
curl -X POST http://localhost:8080/events \
  -H "Content-Type: application/json" \
  -d '{
    "type": "UserCreated",
    "aggregate_id": "user-manual-test",
    "payload": {
      "name": "Jane Doe",
      "email": "jane@example.com"
    }
  }'
```

**4b. Verify the event exists on all nodes:**

Open 3 new terminals and run:

```bash
# Terminal 1 - Query node 0
kubectl run query-node-0 --rm -i --restart=Never \
  --image=curlimages/curl:latest -n event-store -- \
  curl -s http://event-store-0.event-store:8080/events/user-manual-test

# Terminal 2 - Query node 1
kubectl run query-node-1 --rm -i --restart=Never \
  --image=curlimages/curl:latest -n event-store -- \
  curl -s http://event-store-1.event-store:8080/events/user-manual-test

# Terminal 3 - Query node 2
kubectl run query-node-2 --rm -i --restart=Never \
  --image=curlimages/curl:latest -n event-store -- \
  curl -s http://event-store-2.event-store:8080/events/user-manual-test
```

**All 3 queries should return the same event!** ✅

**4c. Check WAL files on all nodes:**
```bash
# Node 0
kubectl exec -n event-store event-store-0 -- ls -lh /data/wal/

# Node 1
kubectl exec -n event-store event-store-1 -- ls -lh /data/wal/

# Node 2
kubectl exec -n event-store event-store-2 -- ls -lh /data/wal/
```

All nodes should have similar WAL files with the same entries.

#### 5. View Replication Logs

**Watch logs showing replication activity:**
```bash
# View logs from all event-store pods
kubectl logs -n event-store -l app=event-store -f --tail=50

# Or view specific node
kubectl logs -n event-store event-store-0 -f
```

Look for log entries like:
```
INFO	replicated event	{"index": 1, "acks": 3}
INFO	committed entries	{"commit_index": 1}
```

#### 6. Test Leader Redirect (Write to Follower)

**Find which node is NOT the leader:**
```bash
kubectl run find-follower --rm -i --restart=Never \
  --image=curlimages/curl:latest -n event-store -- \
  sh -c 'curl -s http://event-store-1.event-store:8080/status | grep -o "\"is_leader\":[^,]*"'
```

**Try to write to a follower (e.g., event-store-1):**
```bash
kubectl run write-to-follower --rm -i --restart=Never \
  --image=curlimages/curl:latest -n event-store -- \
  curl -s -X POST http://event-store-1.event-store:8080/events \
  -H "Content-Type: application/json" \
  -d '{"type": "TestRedirect", "aggregate_id": "redirect-test", "payload": {}}'
```

**Expected response (307 Redirect):**
```json
{
  "message": "not the leader, redirect to leader",
  "leader_id": "event-store-0",
  "leader_address": "event-store-0.event-store.event-store.svc.cluster.local:8080"
}
```

#### 7. Load Test Replication

```bash
# Create 50 events and watch replication
for i in {1..50}; do
  curl -X POST http://localhost:8080/events \
    -H "Content-Type: application/json" \
    -d "{
      \"type\": \"LoadTest\",
      \"aggregate_id\": \"load-test-$i\",
      \"payload\": {
        \"iteration\": $i,
        \"timestamp\": $(date +%s)
      }
    }" -s -o /dev/null -w "Event $i: %{http_code}\n"
  sleep 0.1
done

# Check event count on all nodes (should be the same)
for i in 0 1 2; do
  echo "Node $i event count:"
  kubectl run count-node-$i --rm -i --restart=Never \
    --image=curlimages/curl:latest -n event-store -- \
    curl -s http://event-store-$i.event-store:8080/events | grep -o '"count":[0-9]*'
done
```

#### 8. Test Leader Failure and Automatic Failover

**Delete the current leader:**
```bash
# First, identify the leader
kubectl run find-leader --rm -i --restart=Never \
  --image=curlimages/curl:latest -n event-store -- \
  curl -s http://event-store-external:8080/status | grep -o '"id":"[^"]*"'

# Assuming event-store-0 is leader, delete it
kubectl delete pod -n event-store event-store-0

# Watch the election happen
kubectl logs -n event-store -l app=event-store -f | grep -E "(campaign|leader|election)"
```

**Verify new leader was elected:**
```bash
# Wait a few seconds for election
sleep 5

# Check new leader
curl -s http://localhost:8080/status | grep -o '"is_leader":[^,]*,"leader_id":"[^"]*"'
```

**Write event to new leader:**
```bash
curl -X POST http://localhost:8080/events \
  -H "Content-Type: application/json" \
  -d '{
    "type": "AfterFailover",
    "aggregate_id": "failover-test",
    "payload": {"message": "new leader handling writes"}
  }'
```

**When old leader comes back, verify it becomes follower and syncs:**
```bash
# Wait for pod to restart
kubectl wait --for=condition=ready pod/event-store-0 -n event-store --timeout=120s

# Check its status (should be follower now)
kubectl run check-recovered --rm -i --restart=Never \
  --image=curlimages/curl:latest -n event-store -- \
  curl -s http://event-store-0.event-store:8080/status

# Verify it has the failover event
kubectl run check-sync --rm -i --restart=Never \
  --image=curlimages/curl:latest -n event-store -- \
  curl -s http://event-store-0.event-store:8080/events/failover-test
```

### Quick Commands Reference

```bash
# Deploy everything
make k8s-deploy

# Port forward
make k8s-port-forward

# Run comprehensive replication test
make k8s-test-replication

# Check pod status
make k8s-status

# View all logs
kubectl logs -n event-store -l app=event-store -f

# Clean up
make k8s-delete
```
