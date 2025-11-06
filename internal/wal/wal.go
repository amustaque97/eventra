package wal

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/amustaque97/eventra/internal/models"
	"go.uber.org/zap"
)

const (
	// DefaultSegmentSize is the default size for WAL segments (512MB)
	DefaultSegmentSize = 512 * 1024 * 1024

	// WAL file naming: wal-{term}-{start-index}.log
	walFilePattern = "wal-%010d-%010d.log"
)

// WAL represents the Write-Ahead Log
type WAL struct {
	mu             sync.RWMutex
	dataDir        string
	currentFile    *os.File
	currentWriter  *bufio.Writer
	currentTerm    int64
	startIndex     int64
	currentSize    int64
	maxSegmentSize int64
	logger         *zap.Logger
}

// NewWAL creates a new WAL instance
func NewWAL(dataDir string, logger *zap.Logger) (*WAL, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	wal := &WAL{
		dataDir:        dataDir,
		maxSegmentSize: DefaultSegmentSize,
		logger:         logger,
	}

	return wal, nil
}

// Append writes a log entry to the WAL
func (w *WAL) Append(entry *models.LogEntry) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.appendLocked(entry)
}

// appendLocked appends an entry without acquiring the lock (internal use)
func (w *WAL) appendLocked(entry *models.LogEntry) error {
	// Check if we need to rotate the segment
	if w.currentFile == nil || w.currentSize >= w.maxSegmentSize || entry.Term != w.currentTerm {
		if err := w.rotateSegment(entry.Term, entry.Index); err != nil {
			return fmt.Errorf("failed to rotate segment: %w", err)
		}
	}

	// Serialize the entry
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to marshal entry: %w", err)
	}

	// Calculate checksum
	checksum := crc32.ChecksumIEEE(data)
	entry.Checksum = checksum

	// Re-marshal with checksum
	data, err = json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to marshal entry with checksum: %w", err)
	}

	// Write length prefix (4 bytes)
	length := uint32(len(data))
	if err := binary.Write(w.currentWriter, binary.LittleEndian, length); err != nil {
		return fmt.Errorf("failed to write length: %w", err)
	}

	// Write data
	if _, err := w.currentWriter.Write(data); err != nil {
		return fmt.Errorf("failed to write data: %w", err)
	}

	// Flush to ensure data is written
	if err := w.currentWriter.Flush(); err != nil {
		return fmt.Errorf("failed to flush: %w", err)
	}

	// Fsync to ensure durability
	if err := w.currentFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync: %w", err)
	}

	w.currentSize += int64(4 + length)

	return nil
}

// rotateSegment creates a new WAL segment file
func (w *WAL) rotateSegment(term, startIndex int64) error {
	// Close current file if open
	if w.currentFile != nil {
		if err := w.currentWriter.Flush(); err != nil {
			return err
		}
		if err := w.currentFile.Close(); err != nil {
			return err
		}
	}

	// Create new file
	filename := fmt.Sprintf(walFilePattern, term, startIndex)
	filepath := filepath.Join(w.dataDir, filename)

	file, err := os.OpenFile(filepath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to create WAL file: %w", err)
	}

	w.currentFile = file
	w.currentWriter = bufio.NewWriter(file)
	w.currentTerm = term
	w.startIndex = startIndex
	w.currentSize = 0

	w.logger.Info("rotated WAL segment",
		zap.String("filename", filename),
		zap.Int64("term", term),
		zap.Int64("start_index", startIndex))

	return nil
}

// ReadAll reads all log entries from all WAL segments
func (w *WAL) ReadAll() ([]*models.LogEntry, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.readAllLocked()
}

// readAllLocked reads all log entries without acquiring the lock (internal use)
func (w *WAL) readAllLocked() ([]*models.LogEntry, error) {
	// Get all WAL files
	files, err := w.getWALFiles()
	if err != nil {
		return nil, err
	}

	var entries []*models.LogEntry

	for _, filename := range files {
		fileEntries, err := w.readFile(filename)
		if err != nil {
			w.logger.Error("failed to read WAL file",
				zap.String("filename", filename),
				zap.Error(err))
			continue
		}
		entries = append(entries, fileEntries...)
	}

	return entries, nil
}

// ReadFrom reads log entries starting from a specific index
func (w *WAL) ReadFrom(fromIndex int64) ([]*models.LogEntry, error) {
	allEntries, err := w.ReadAll()
	if err != nil {
		return nil, err
	}

	var entries []*models.LogEntry
	for _, entry := range allEntries {
		if entry.Index >= fromIndex {
			entries = append(entries, entry)
		}
	}

	return entries, nil
}

// readFile reads all entries from a single WAL file
func (w *WAL) readFile(filename string) ([]*models.LogEntry, error) {
	filepath := filepath.Join(w.dataDir, filename)

	file, err := os.Open(filepath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	var entries []*models.LogEntry

	for {
		// Read length prefix
		var length uint32
		if err := binary.Read(reader, binary.LittleEndian, &length); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("failed to read length: %w", err)
		}

		// Read data
		data := make([]byte, length)
		if _, err := io.ReadFull(reader, data); err != nil {
			return nil, fmt.Errorf("failed to read data: %w", err)
		}

		// Unmarshal entry
		var entry models.LogEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			return nil, fmt.Errorf("failed to unmarshal entry: %w", err)
		}

		// Verify checksum
		entryCopy := entry
		entryCopy.Checksum = 0
		verifyData, _ := json.Marshal(&entryCopy)
		expectedChecksum := crc32.ChecksumIEEE(verifyData)

		if entry.Checksum != expectedChecksum {
			w.logger.Warn("checksum mismatch",
				zap.Int64("index", entry.Index),
				zap.Uint32("expected", expectedChecksum),
				zap.Uint32("actual", entry.Checksum))
			// Continue reading despite checksum mismatch (could be configurable)
		}

		entries = append(entries, &entry)
	}

	return entries, nil
}

// getWALFiles returns all WAL files sorted by term and start index
func (w *WAL) getWALFiles() ([]string, error) {
	files, err := os.ReadDir(w.dataDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	var walFiles []string
	for _, file := range files {
		if !file.IsDir() && strings.HasPrefix(file.Name(), "wal-") && strings.HasSuffix(file.Name(), ".log") {
			walFiles = append(walFiles, file.Name())
		}
	}

	// Sort files by name (which naturally sorts by term and start index)
	sort.Strings(walFiles)

	return walFiles, nil
}

// GetLastEntry returns the last log entry in the WAL
func (w *WAL) GetLastEntry() (*models.LogEntry, error) {
	entries, err := w.ReadAll()
	if err != nil {
		return nil, err
	}

	if len(entries) == 0 {
		return nil, nil
	}

	return entries[len(entries)-1], nil
}

// Truncate removes all entries after the specified index
func (w *WAL) Truncate(afterIndex int64) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Read all entries
	entries, err := w.readAllLocked()
	if err != nil {
		return err
	}

	// Find entries to keep
	var toKeep []*models.LogEntry
	for _, entry := range entries {
		if entry.Index <= afterIndex {
			toKeep = append(toKeep, entry)
		}
	}

	// Close current file
	if w.currentFile != nil {
		w.currentWriter.Flush()
		w.currentFile.Close()
	}

	// Remove all WAL files
	files, err := w.getWALFiles()
	if err != nil {
		return err
	}
	for _, filename := range files {
		if err := os.Remove(filepath.Join(w.dataDir, filename)); err != nil {
			w.logger.Error("failed to remove WAL file", zap.String("filename", filename), zap.Error(err))
		}
	}

	// Rewrite kept entries
	w.currentFile = nil
	w.currentWriter = nil
	w.currentSize = 0

	for _, entry := range toKeep {
		if err := w.appendLocked(entry); err != nil {
			return fmt.Errorf("failed to rewrite entry: %w", err)
		}
	}

	return nil
}

// Close closes the WAL
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.currentFile != nil {
		if err := w.currentWriter.Flush(); err != nil {
			return err
		}
		if err := w.currentFile.Close(); err != nil {
			return err
		}
	}

	return nil
}
