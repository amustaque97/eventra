package replication

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/amustaque97/eventra/internal/models"
	"github.com/amustaque97/eventra/internal/store"
	"go.uber.org/zap"
)

const (
	heartbeatInterval  = 150 * time.Millisecond
	replicationTimeout = 5 * time.Second
)

// Follower represents a follower node
type Follower struct {
	ID            string
	Address       string
	NextIndex     int64
	MatchIndex    int64
	LastHeartbeat time.Time
}

// Replicator handles replication between leader and followers
type Replicator struct {
	mu              sync.RWMutex
	store           *store.EventStore
	nodeID          string
	isLeader        bool
	leaderID        string
	leaderAddress   string
	followers       map[string]*Follower
	httpClient      *http.Client
	logger          *zap.Logger
	stopCh          chan struct{}
	heartbeatTicker *time.Ticker
}

// NewReplicator creates a new replicator
func NewReplicator(nodeID string, store *store.EventStore, logger *zap.Logger) *Replicator {
	return &Replicator{
		store:      store,
		nodeID:     nodeID,
		followers:  make(map[string]*Follower),
		httpClient: &http.Client{Timeout: replicationTimeout},
		logger:     logger,
		stopCh:     make(chan struct{}),
	}
}

// IsLeader returns true if this node is the leader
func (r *Replicator) IsLeader() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.isLeader
}

// GetLeaderID returns the current leader ID
func (r *Replicator) GetLeaderID() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.leaderID
}

// GetLeaderAddress returns the current leader address
func (r *Replicator) GetLeaderAddress() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.leaderAddress
}

// BecomeLeader transitions this node to leader role
func (r *Replicator) BecomeLeader(term int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.isLeader = true
	r.leaderID = r.nodeID
	r.store.SetCurrentTerm(term)

	// Initialize follower indices
	lastApplied := r.store.GetLastApplied()
	for _, follower := range r.followers {
		follower.NextIndex = lastApplied + 1
		follower.MatchIndex = 0
	}

	r.logger.Info("became leader", zap.Int64("term", term))

	// Start heartbeat loop
	r.startHeartbeat()
}

// BecomeFollower transitions this node to follower role
func (r *Replicator) BecomeFollower(leaderID, leaderAddress string, term int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.isLeader = false
	r.leaderID = leaderID
	r.leaderAddress = leaderAddress
	r.store.SetCurrentTerm(term)

	r.logger.Info("became follower",
		zap.String("leader_id", leaderID),
		zap.Int64("term", term))

	// Stop heartbeat if running
	r.stopHeartbeat()
}

// AddFollower adds a follower node
func (r *Replicator) AddFollower(id, address string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if id == r.nodeID {
		return // Don't add self
	}

	lastApplied := r.store.GetLastApplied()
	r.followers[id] = &Follower{
		ID:            id,
		Address:       address,
		NextIndex:     lastApplied + 1,
		MatchIndex:    0,
		LastHeartbeat: time.Now(),
	}

	r.logger.Info("added follower",
		zap.String("follower_id", id),
		zap.String("address", address))
}

// RemoveFollower removes a follower node
func (r *Replicator) RemoveFollower(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.followers, id)
	r.logger.Info("removed follower", zap.String("follower_id", id))
}

// ReplicateEvent replicates a specific event to all followers
func (r *Replicator) ReplicateEvent(index int64) error {
	if !r.IsLeader() {
		return fmt.Errorf("not the leader")
	}

	r.mu.RLock()
	followers := make([]*Follower, 0, len(r.followers))
	for _, f := range r.followers {
		followers = append(followers, f)
	}
	r.mu.RUnlock()

	// Wait for majority acknowledgment
	ackCount := 1 // Leader counts as ack
	ackCh := make(chan bool, len(followers))

	for _, follower := range followers {
		go func(f *Follower) {
			success := r.replicateToFollower(f, index)
			ackCh <- success
		}(follower)
	}

	// Wait for majority (including leader)
	requiredAcks := (len(followers)+1)/2 + 1
	timeout := time.After(replicationTimeout)

	for ackCount < requiredAcks {
		select {
		case success := <-ackCh:
			if success {
				ackCount++
			}
		case <-timeout:
			r.logger.Warn("replication timeout",
				zap.Int64("index", index),
				zap.Int("ack_count", ackCount),
				zap.Int("required", requiredAcks))
			return fmt.Errorf("replication timeout")
		}
	}

	// Commit the entry
	if err := r.store.CommitUpTo(index); err != nil {
		return fmt.Errorf("failed to commit: %w", err)
	}

	r.logger.Debug("replicated event",
		zap.Int64("index", index),
		zap.Int("acks", ackCount))

	return nil
}

// replicateToFollower replicates entries to a specific follower
func (r *Replicator) replicateToFollower(follower *Follower, upToIndex int64) bool {
	r.mu.RLock()
	nextIndex := follower.NextIndex
	r.mu.RUnlock()

	// Get entries to send
	var entries []*models.LogEntry
	for i := nextIndex; i <= upToIndex; i++ {
		entry, exists := r.store.GetEntry(i)
		if !exists {
			r.logger.Error("entry not found", zap.Int64("index", i))
			return false
		}
		entries = append(entries, entry)
	}

	if len(entries) == 0 {
		return true // Nothing to replicate
	}

	// Get previous log entry info
	var prevLogIndex int64
	var prevLogTerm int64
	if nextIndex > 1 {
		prevEntry, exists := r.store.GetEntry(nextIndex - 1)
		if exists {
			prevLogIndex = prevEntry.Index
			prevLogTerm = prevEntry.Term
		}
	}

	// Create AppendEntries request
	req := &models.AppendEntriesRequest{
		Term:              r.store.GetCurrentTerm(),
		LeaderID:          r.nodeID,
		PrevLogIndex:      prevLogIndex,
		PrevLogTerm:       prevLogTerm,
		Entries:           entries,
		LeaderCommitIndex: r.store.GetCommitIndex(),
	}

	// Send request to follower
	resp, err := r.sendAppendEntries(follower.Address, req)
	if err != nil {
		r.logger.Error("failed to send append entries",
			zap.String("follower", follower.ID),
			zap.Error(err))
		return false
	}

	if !resp.Success {
		// Decrement nextIndex and retry
		r.mu.Lock()
		if follower.NextIndex > 1 {
			follower.NextIndex--
		}
		r.mu.Unlock()
		r.logger.Debug("append entries rejected, retrying",
			zap.String("follower", follower.ID),
			zap.Int64("next_index", follower.NextIndex))
		return false
	}

	// Update follower indices
	r.mu.Lock()
	follower.NextIndex = upToIndex + 1
	follower.MatchIndex = upToIndex
	follower.LastHeartbeat = time.Now()
	r.mu.Unlock()

	return true
}

// sendAppendEntries sends an AppendEntries HTTP request
func (r *Replicator) sendAppendEntries(address string, req *models.AppendEntriesRequest) (*models.AppendEntriesResponse, error) {
	url := fmt.Sprintf("http://%s/internal/append-entries", address)

	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := r.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", httpResp.StatusCode)
	}

	var resp models.AppendEntriesResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&resp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &resp, nil
}

// startHeartbeat starts sending periodic heartbeats to followers
func (r *Replicator) startHeartbeat() {
	r.heartbeatTicker = time.NewTicker(heartbeatInterval)

	go func() {
		for {
			select {
			case <-r.heartbeatTicker.C:
				r.sendHeartbeats()
			case <-r.stopCh:
				return
			}
		}
	}()
}

// stopHeartbeat stops the heartbeat ticker
func (r *Replicator) stopHeartbeat() {
	if r.heartbeatTicker != nil {
		r.heartbeatTicker.Stop()
	}
}

// sendHeartbeats sends heartbeat messages to all followers
func (r *Replicator) sendHeartbeats() {
	if !r.IsLeader() {
		return
	}

	r.mu.RLock()
	followers := make([]*Follower, 0, len(r.followers))
	for _, f := range r.followers {
		followers = append(followers, f)
	}
	lastApplied := r.store.GetLastApplied()
	r.mu.RUnlock()

	for _, follower := range followers {
		go func(f *Follower) {
			// Replicate any missing entries
			if f.NextIndex <= lastApplied {
				r.replicateToFollower(f, lastApplied)
			} else {
				// Send empty heartbeat
				r.replicateToFollower(f, f.NextIndex-1)
			}
		}(follower)
	}

	// Update commit index based on follower match indices
	r.updateCommitIndex()
}

// updateCommitIndex advances the commit index based on follower match indices
func (r *Replicator) updateCommitIndex() {
	if !r.IsLeader() {
		return
	}

	r.mu.RLock()
	matchIndices := make([]int64, 0, len(r.followers)+1)
	matchIndices = append(matchIndices, r.store.GetLastApplied()) // Leader's index
	for _, f := range r.followers {
		matchIndices = append(matchIndices, f.MatchIndex)
	}
	r.mu.RUnlock()

	// Sort match indices
	sort.Slice(matchIndices, func(i, j int) bool {
		return matchIndices[i] < matchIndices[j]
	})

	// Find the median (majority consensus)
	majorityIndex := matchIndices[len(matchIndices)/2]

	// Only advance commit index, never decrease
	currentCommit := r.store.GetCommitIndex()
	if majorityIndex > currentCommit {
		if err := r.store.CommitUpTo(majorityIndex); err != nil {
			r.logger.Error("failed to update commit index", zap.Error(err))
		} else {
			r.logger.Debug("advanced commit index",
				zap.Int64("from", currentCommit),
				zap.Int64("to", majorityIndex))
		}
	}
}

// Stop stops the replicator
func (r *Replicator) Stop() {
	close(r.stopCh)
	r.stopHeartbeat()
}
