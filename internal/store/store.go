package store

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/amustaque97/eventra/internal/models"
	"github.com/amustaque97/eventra/internal/wal"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// EventStore manages the in-memory state and coordinates with WAL
type EventStore struct {
	mu          sync.RWMutex
	wal         *wal.WAL
	events      map[string]*models.Event   // event_id -> event
	byAggregate map[string][]*models.Event // aggregate_id -> events
	byIndex     map[int64]*models.LogEntry // index -> log entry
	snapshots   map[int64]*models.Snapshot // index -> snapshot
	lastApplied int64
	commitIndex int64
	currentTerm int64
	logger      *zap.Logger
}

// NewEventStore creates a new event store
func NewEventStore(walDir string, logger *zap.Logger) (*EventStore, error) {
	w, err := wal.NewWAL(walDir, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create WAL: %w", err)
	}

	store := &EventStore{
		wal:         w,
		events:      make(map[string]*models.Event),
		byAggregate: make(map[string][]*models.Event),
		byIndex:     make(map[int64]*models.LogEntry),
		snapshots:   make(map[int64]*models.Snapshot),
		logger:      logger,
	}

	// Recover from WAL
	if err := store.recover(); err != nil {
		return nil, fmt.Errorf("failed to recover from WAL: %w", err)
	}

	return store, nil
}

// recover replays WAL entries to rebuild in-memory state
func (s *EventStore) recover() error {
	s.logger.Info("starting recovery from WAL")

	entries, err := s.wal.ReadAll()
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if err := s.applyEntry(entry); err != nil {
			s.logger.Error("failed to apply entry during recovery",
				zap.Int64("index", entry.Index),
				zap.Error(err))
			continue
		}
	}

	s.logger.Info("recovery completed",
		zap.Int64("last_applied", s.lastApplied),
		zap.Int("event_count", len(s.events)))

	return nil
}

// WriteEvent writes a new event (leader only)
func (s *EventStore) WriteEvent(req *models.WriteEventRequest, term int64) (*models.WriteEventResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Create event
	event := &models.Event{
		ID:          uuid.New().String(),
		Type:        req.Type,
		Timestamp:   time.Now().Unix(),
		AggregateID: req.AggregateID,
		Payload:     req.Payload,
		Metadata:    req.Metadata,
	}

	// Create log entry
	nextIndex := s.lastApplied + 1
	logEntry := &models.LogEntry{
		Term:      term,
		Index:     nextIndex,
		EntryType: models.LogEntryTypeEvent,
		Event:     event,
		Timestamp: time.Now().Unix(),
	}

	// Append to WAL first (durability)
	if err := s.wal.Append(logEntry); err != nil {
		return nil, fmt.Errorf("failed to append to WAL: %w", err)
	}

	// Store in memory (will be committed after replication)
	s.byIndex[nextIndex] = logEntry
	s.lastApplied = nextIndex

	// Don't apply to state machine yet - wait for commit

	return &models.WriteEventResponse{
		Success: true,
		EventID: event.ID,
		Index:   nextIndex,
	}, nil
}

// CommitUpTo commits all entries up to the specified index
func (s *EventStore) CommitUpTo(index int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.commitUpToLocked(index)
}

// commitUpToLocked commits entries without acquiring the lock (internal use)
func (s *EventStore) commitUpToLocked(index int64) error {
	for i := s.commitIndex + 1; i <= index; i++ {
		entry, exists := s.byIndex[i]
		if !exists {
			continue
		}

		if entry.EntryType == models.LogEntryTypeEvent && entry.Event != nil {
			// Apply to state machine
			s.events[entry.Event.ID] = entry.Event
			s.byAggregate[entry.Event.AggregateID] = append(
				s.byAggregate[entry.Event.AggregateID],
				entry.Event,
			)
		}
	}

	s.commitIndex = index
	s.logger.Debug("committed entries", zap.Int64("commit_index", index))

	return nil
}

// applyEntry applies a log entry to the in-memory state
func (s *EventStore) applyEntry(entry *models.LogEntry) error {
	s.byIndex[entry.Index] = entry

	if entry.Index > s.lastApplied {
		s.lastApplied = entry.Index
	}

	if entry.Index > s.commitIndex {
		s.commitIndex = entry.Index
	}

	if entry.Term > s.currentTerm {
		s.currentTerm = entry.Term
	}

	if entry.EntryType == models.LogEntryTypeEvent && entry.Event != nil {
		s.events[entry.Event.ID] = entry.Event
		s.byAggregate[entry.Event.AggregateID] = append(
			s.byAggregate[entry.Event.AggregateID],
			entry.Event,
		)
	}

	return nil
}

// AppendEntries appends entries from replication (follower only)
func (s *EventStore) AppendEntries(req *models.AppendEntriesRequest) (*models.AppendEntriesResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Update term if newer
	if req.Term > s.currentTerm {
		s.currentTerm = req.Term
	}

	// Check if we have the previous entry
	if req.PrevLogIndex > 0 {
		prevEntry, exists := s.byIndex[req.PrevLogIndex]
		if !exists || prevEntry.Term != req.PrevLogTerm {
			return &models.AppendEntriesResponse{
				Term:             s.currentTerm,
				Success:          false,
				LastAppliedIndex: s.lastApplied,
			}, nil
		}
	}

	// Append new entries
	for _, entry := range req.Entries {
		// Check for conflicts
		if existingEntry, exists := s.byIndex[entry.Index]; exists {
			if existingEntry.Term != entry.Term {
				// Conflict: delete this and all following entries
				if err := s.wal.Truncate(entry.Index - 1); err != nil {
					return nil, fmt.Errorf("failed to truncate WAL: %w", err)
				}
				// Clear in-memory state for conflicting entries
				s.truncateMemory(entry.Index - 1)
			} else {
				// Entry already exists with same term, skip
				continue
			}
		}

		// Append to WAL
		if err := s.wal.Append(entry); err != nil {
			return nil, fmt.Errorf("failed to append to WAL: %w", err)
		}

		// Store in memory
		s.byIndex[entry.Index] = entry
		if entry.Index > s.lastApplied {
			s.lastApplied = entry.Index
		}
	}

	// Update commit index
	if req.LeaderCommitIndex > s.commitIndex {
		newCommitIndex := req.LeaderCommitIndex
		if s.lastApplied < newCommitIndex {
			newCommitIndex = s.lastApplied
		}
		s.commitUpToLocked(newCommitIndex)
	}

	return &models.AppendEntriesResponse{
		Term:             s.currentTerm,
		Success:          true,
		LastAppliedIndex: s.lastApplied,
	}, nil
}

// truncateMemory removes entries from memory after the specified index
func (s *EventStore) truncateMemory(afterIndex int64) {
	// Remove from byIndex
	for i := afterIndex + 1; i <= s.lastApplied; i++ {
		delete(s.byIndex, i)
	}

	// Rebuild events and byAggregate from remaining entries
	s.events = make(map[string]*models.Event)
	s.byAggregate = make(map[string][]*models.Event)

	for i := int64(1); i <= afterIndex; i++ {
		if entry, exists := s.byIndex[i]; exists && entry.Event != nil {
			s.events[entry.Event.ID] = entry.Event
			s.byAggregate[entry.Event.AggregateID] = append(
				s.byAggregate[entry.Event.AggregateID],
				entry.Event,
			)
		}
	}

	s.lastApplied = afterIndex
	if s.commitIndex > afterIndex {
		s.commitIndex = afterIndex
	}
}

// ReadEvents reads events by aggregate ID
func (s *EventStore) ReadEvents(req *models.ReadEventsRequest) (*models.ReadEventsResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var events []*models.Event

	if req.AggregateID != "" {
		// Get events for specific aggregate
		events = s.byAggregate[req.AggregateID]
	} else {
		// Get all events
		for _, event := range s.events {
			events = append(events, event)
		}
	}

	// Filter by index range if specified
	if req.FromIndex > 0 || req.ToIndex > 0 {
		var filtered []*models.Event
		for idx := req.FromIndex; idx <= req.ToIndex && idx <= s.lastApplied; idx++ {
			if entry, exists := s.byIndex[idx]; exists && entry.Event != nil {
				filtered = append(filtered, entry.Event)
			}
		}
		events = filtered
	}

	// Apply limit
	if req.Limit > 0 && len(events) > req.Limit {
		events = events[:req.Limit]
	}

	return &models.ReadEventsResponse{
		Events: events,
		Count:  len(events),
	}, nil
}

// GetLastApplied returns the index of the last applied entry
func (s *EventStore) GetLastApplied() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastApplied
}

// GetCommitIndex returns the current commit index
func (s *EventStore) GetCommitIndex() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.commitIndex
}

// GetCurrentTerm returns the current term
func (s *EventStore) GetCurrentTerm() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentTerm
}

// SetCurrentTerm sets the current term
func (s *EventStore) SetCurrentTerm(term int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentTerm = term
}

// GetEntry returns a log entry by index
func (s *EventStore) GetEntry(index int64) (*models.LogEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, exists := s.byIndex[index]
	return entry, exists
}

// CreateSnapshot creates a snapshot of the current state
func (s *EventStore) CreateSnapshot() (*models.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	state := map[string]interface{}{
		"events":      s.events,
		"byAggregate": s.byAggregate,
	}

	stateBytes, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal state: %w", err)
	}

	snapshot := &models.Snapshot{
		Index:     s.commitIndex,
		Term:      s.currentTerm,
		State:     stateBytes,
		Timestamp: time.Now().Unix(),
	}

	s.snapshots[s.commitIndex] = snapshot

	return snapshot, nil
}

// Close closes the event store
func (s *EventStore) Close() error {
	return s.wal.Close()
}
