package models

import (
	"encoding/json"
	"time"
)

// Event represents a single event in the event store
type Event struct {
	ID          string            `json:"id"`
	Type        string            `json:"type"`
	Timestamp   int64             `json:"timestamp"`
	AggregateID string            `json:"aggregate_id"`
	Payload     json.RawMessage   `json:"payload"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// LogEntryType defines the type of log entry
type LogEntryType int

const (
	LogEntryTypeEvent LogEntryType = iota
	LogEntryTypeCheckpoint
	LogEntryTypeSnapshot
)

// LogEntry represents a single entry in the WAL
type LogEntry struct {
	Term      int64        `json:"term"`
	Index     int64        `json:"index"`
	EntryType LogEntryType `json:"entry_type"`
	Event     *Event       `json:"event,omitempty"`
	Checksum  uint32       `json:"checksum"`
	Timestamp int64        `json:"timestamp"`
}

// Snapshot represents a state snapshot at a specific point in time
type Snapshot struct {
	Index     int64  `json:"index"`
	Term      int64  `json:"term"`
	State     []byte `json:"state"`
	Timestamp int64  `json:"timestamp"`
}

// AppendEntriesRequest represents a replication request from leader to follower
type AppendEntriesRequest struct {
	Term              int64       `json:"term"`
	LeaderID          string      `json:"leader_id"`
	PrevLogIndex      int64       `json:"prev_log_index"`
	PrevLogTerm       int64       `json:"prev_log_term"`
	Entries           []*LogEntry `json:"entries"`
	LeaderCommitIndex int64       `json:"leader_commit_index"`
}

// AppendEntriesResponse represents the response from follower to leader
type AppendEntriesResponse struct {
	Term             int64  `json:"term"`
	Success          bool   `json:"success"`
	LastAppliedIndex int64  `json:"last_applied_index"`
	FollowerID       string `json:"follower_id"`
}

// WriteEventRequest represents a client request to write an event
type WriteEventRequest struct {
	Type        string            `json:"type"`
	AggregateID string            `json:"aggregate_id"`
	Payload     json.RawMessage   `json:"payload"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// WriteEventResponse represents the response to a write request
type WriteEventResponse struct {
	Success bool   `json:"success"`
	EventID string `json:"event_id"`
	Index   int64  `json:"index"`
	Message string `json:"message,omitempty"`
}

// ReadEventsRequest represents a request to read events
type ReadEventsRequest struct {
	AggregateID string `json:"aggregate_id,omitempty"`
	FromIndex   int64  `json:"from_index,omitempty"`
	ToIndex     int64  `json:"to_index,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

// ReadEventsResponse represents the response containing events
type ReadEventsResponse struct {
	Events []*Event `json:"events"`
	Count  int      `json:"count"`
}

// NodeInfo represents information about a cluster node
type NodeInfo struct {
	ID            string    `json:"id"`
	IsLeader      bool      `json:"is_leader"`
	LeaderID      string    `json:"leader_id"`
	Term          int64     `json:"term"`
	CommitIndex   int64     `json:"commit_index"`
	LastApplied   int64     `json:"last_applied"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
}

// HealthResponse represents the health status of a node
type HealthResponse struct {
	Status    string    `json:"status"`
	NodeID    string    `json:"node_id"`
	IsLeader  bool      `json:"is_leader"`
	LeaderID  string    `json:"leader_id"`
	Timestamp time.Time `json:"timestamp"`
}
