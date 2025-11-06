#!/bin/bash

# Simple load test script for the event store

set -e

# Configuration
BASE_URL="${BASE_URL:-http://localhost:8080}"
NUM_REQUESTS="${NUM_REQUESTS:-100}"
CONCURRENT="${CONCURRENT:-5}"

echo "🚀 Event Store Load Test"
echo "========================"
echo "Base URL: $BASE_URL"
echo "Requests: $NUM_REQUESTS"
echo "Concurrent: $CONCURRENT"
echo ""

# Check if server is running
echo "⏳ Checking server health..."
if ! curl -sf "$BASE_URL/health" > /dev/null; then
    echo "❌ Error: Server is not responding at $BASE_URL"
    exit 1
fi
echo "✅ Server is healthy"
echo ""

# Function to create an event
create_event() {
    local id=$1
    curl -s -X POST "$BASE_URL/events" \
        -H "Content-Type: application/json" \
        -d "{
            \"type\": \"LoadTest\",
            \"aggregate_id\": \"load-test-$id\",
            \"payload\": {
                \"iteration\": $id,
                \"timestamp\": $(date +%s)
            }
        }" -w "%{http_code}" -o /dev/null
}

# Create a temporary directory for concurrent requests
temp_dir=$(mktemp -d)
trap "rm -rf $temp_dir" EXIT

echo "📊 Starting load test..."
start_time=$(date +%s)

# Run requests
success_count=0
failure_count=0

for ((i=1; i<=NUM_REQUESTS; i++)); do
    # Run concurrent requests
    if (( i % CONCURRENT == 0 )); then
        wait
        echo "Progress: $i/$NUM_REQUESTS requests completed"
    fi
    
    (
        status=$(create_event $i)
        if [[ "$status" == "201" ]]; then
            echo "1" > "$temp_dir/success_$i"
        else
            echo "1" > "$temp_dir/failure_$i"
        fi
    ) &
done

# Wait for all background jobs
wait

# Count results
success_count=$(ls $temp_dir/success_* 2>/dev/null | wc -l)
failure_count=$(ls $temp_dir/failure_* 2>/dev/null | wc -l)

end_time=$(date +%s)
duration=$((end_time - start_time))

echo ""
echo "✨ Load Test Complete!"
echo "======================"
echo "Duration: ${duration}s"
echo "Success: $success_count"
echo "Failure: $failure_count"
echo "Success Rate: $(awk "BEGIN {printf \"%.2f\", ($success_count/$NUM_REQUESTS)*100}")%"
echo "Throughput: $(awk "BEGIN {printf \"%.2f\", $NUM_REQUESTS/$duration}") req/s"
echo ""

# Get final status
echo "📈 Final Server Status:"
curl -s "$BASE_URL/status" | jq '.'
