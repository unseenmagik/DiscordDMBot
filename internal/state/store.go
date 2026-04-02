package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type DeliveryRecord struct {
	UserID         string `json:"userId"`
	Date           string `json:"date"`
	Time           string `json:"time"`
	Value          string `json:"value"`
	Message        string `json:"message"`
	DeliveredAtUTC string `json:"deliveredAtUtc"`
}

type FileState struct {
	Deliveries map[string]DeliveryRecord `json:"deliveries"`
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

func (s *Store) Save(fileState *FileState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

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
