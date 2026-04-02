package logging

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type DailyRotatingWriter struct {
	dir         string
	location    *time.Location
	baseName    string
	mu          sync.Mutex
	currentDate string
	file        *os.File
}

func NewLogger(dir, baseName string, location *time.Location) (*log.Logger, io.Closer, error) {
	writer := &DailyRotatingWriter{
		dir:      dir,
		location: location,
		baseName: baseName,
	}

	timestamped := &timestampedWriter{
		location: location,
		out:      io.MultiWriter(os.Stdout, writer),
	}

	return log.New(timestamped, "", 0), writer, nil
}

func (w *DailyRotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.rotateIfNeeded(); err != nil {
		return 0, err
	}

	if w.file == nil {
		return 0, fmt.Errorf("log file is not open")
	}

	if _, err := w.file.Write(p); err != nil {
		return 0, err
	}

	return len(p), nil
}

func (w *DailyRotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return nil
	}

	err := w.file.Close()
	w.file = nil
	return err
}

func (w *DailyRotatingWriter) rotateIfNeeded() error {
	date := time.Now().In(w.location).Format("2006-01-02")
	if w.file != nil && date == w.currentDate {
		return nil
	}

	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}

	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return fmt.Errorf("close log file: %w", err)
		}
	}

	path := filepath.Join(w.dir, fmt.Sprintf("%s-%s.log", w.baseName, date))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}

	w.file = file
	w.currentDate = date
	return nil
}

type timestampedWriter struct {
	location *time.Location
	out      io.Writer
	mu       sync.Mutex
}

func (w *timestampedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	lines := bytes.SplitAfter(p, []byte("\n"))
	var output []byte
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}

		timestamp := time.Now().In(w.location).Format("2006/01/02 15:04:05 MST")
		output = append(output, []byte(timestamp+" ")...)
		output = append(output, line...)
	}

	if _, err := w.out.Write(output); err != nil {
		return 0, err
	}

	return len(p), nil
}
