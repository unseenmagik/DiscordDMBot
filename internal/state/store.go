package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type DeliveryRecord struct {
	UserID         string `json:"userId"`
	Date           string `json:"date"`
	Time           string `json:"time"`
	DueDate        string `json:"dueDate,omitempty"`
	DueTime        string `json:"dueTime,omitempty"`
	ReminderName   string `json:"reminderName,omitempty"`
	Value          string `json:"value"`
	Message        string `json:"message"`
	DeliveredAtUTC string `json:"deliveredAtUtc"`
}

type FileState struct {
	Deliveries map[string]DeliveryRecord `json:"deliveries"`
}

type ClearFilter struct {
	DeliveryID string
	ReminderID string
	DueDate    string
}

type Store struct {
	path string
	mu   sync.Mutex
}

func NewStore(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Load() (*FileState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.loadUnlocked()
}

func (s *Store) Save(fileState *FileState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.saveUnlocked(fileState)
}

func (s *Store) ClearForDeliveryID(deliveryID string) (int, error) {
	return s.ClearMatching(ClearFilter{DeliveryID: deliveryID})
}

func (s *Store) ClearMatching(filter ClearFilter) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	fileState, err := s.loadUnlocked()
	if err != nil {
		return 0, err
	}

	removed := 0
	for stateKey, record := range fileState.Deliveries {
		if matchesFilter(stateKey, record, filter) {
			delete(fileState.Deliveries, stateKey)
			removed++
		}
	}

	if removed == 0 {
		return 0, nil
	}

	if err := s.saveUnlocked(fileState); err != nil {
		return 0, err
	}

	return removed, nil
}

func (s *Store) loadUnlocked() (*FileState, error) {
	content, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &FileState{Deliveries: map[string]DeliveryRecord{}}, nil
		}
		return nil, fmt.Errorf("read state file: %w", err)
	}

	var fileState FileState
	if err := json.Unmarshal(content, &fileState); err != nil {
		return nil, fmt.Errorf("decode state file: %w", err)
	}

	if fileState.Deliveries == nil {
		fileState.Deliveries = map[string]DeliveryRecord{}
	}

	return &fileState, nil
}

func (s *Store) saveUnlocked(fileState *FileState) error {
	if fileState.Deliveries == nil {
		fileState.Deliveries = map[string]DeliveryRecord{}
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}

	content, err := json.MarshalIndent(fileState, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state file: %w", err)
	}

	tempPath := s.path + ".tmp"
	if err := os.WriteFile(tempPath, content, 0o600); err != nil {
		return fmt.Errorf("write temp state file: %w", err)
	}

	if err := os.Rename(tempPath, s.path); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}

	return nil
}

func belongsToDeliveryID(stateKey, deliveryID string) bool {
	if strings.TrimSpace(deliveryID) == "" {
		return false
	}

	return strings.HasPrefix(stateKey, "custom:"+deliveryID) ||
		strings.HasPrefix(stateKey, "reminder:"+deliveryID+":") ||
		strings.HasPrefix(stateKey, "late:"+deliveryID+":")
}

func matchesFilter(stateKey string, record DeliveryRecord, filter ClearFilter) bool {
	deliveryID := strings.TrimSpace(filter.DeliveryID)
	if deliveryID == "" || !belongsToDeliveryID(stateKey, deliveryID) {
		return false
	}

	reminderID := strings.TrimSpace(filter.ReminderID)
	if reminderID == "" {
		return true
	}

	if reminderID == "late" {
		if !strings.HasPrefix(stateKey, "late:"+deliveryID+":") {
			return false
		}
	} else {
		if !strings.HasPrefix(stateKey, "reminder:"+deliveryID+":") {
			return false
		}
		if !strings.HasSuffix(stateKey, ":id:"+reminderID) {
			return false
		}
	}

	dueDate := strings.TrimSpace(filter.DueDate)
	if dueDate == "" {
		return true
	}

	return strings.TrimSpace(record.DueDate) == dueDate
}
