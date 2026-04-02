package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/BurntSushi/toml"
)

type Store struct {
	path string
	mu   sync.Mutex
}

func NewStore(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Load() (*Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.loadUnlocked()
}

func (s *Store) Save(cfg *Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.saveUnlocked(cfg)
}

func (s *Store) AddDelivery(delivery Delivery) (*Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg, err := s.loadUnlocked()
	if err != nil {
		return nil, err
	}

	cfg.Deliveries = append(cfg.Deliveries, delivery)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	if err := s.saveUnlocked(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (s *Store) loadUnlocked() (*Config, error) {
	return Load(s.path)
}

func (s *Store) saveUnlocked(cfg *Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	var buffer bytes.Buffer
	if err := toml.NewEncoder(&buffer).Encode(cfg); err != nil {
		return fmt.Errorf("encode config file: %w", err)
	}

	content := buffer.Bytes()
	if len(content) > 0 && content[len(content)-1] != '\n' {
		content = append(content, '\n')
	}

	tempPath := s.path + ".tmp"
	if err := os.WriteFile(tempPath, content, 0o600); err != nil {
		return fmt.Errorf("write temp config file: %w", err)
	}

	if err := os.Rename(tempPath, s.path); err != nil {
		return fmt.Errorf("replace config file: %w", err)
	}

	return nil
}
