package internal

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// GzipJSONLWriter appends JSON records, one per line, to a gzip-compressed file.
//
// Each record is written as its own complete gzip member. Concatenated gzip members form a
// valid gzip stream, so the file stays readable with zcat, gzip.NewReader, etc. after every
// write and survives the agent being killed or restarted (it is opened in append mode).
type GzipJSONLWriter struct {
	mu sync.Mutex
	f  *os.File
}

// OpenGzipJSONL opens path for appending, creating it and its parent directories if needed.
func OpenGzipJSONL(path string) (*GzipJSONLWriter, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create output directory: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open output file: %w", err)
	}
	return &GzipJSONLWriter{f: f}, nil
}

// Write appends v as a single JSON line.
func (w *GzipJSONLWriter) Write(v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode record: %w", err)
	}

	// Compress into memory first so a failure can't leave a half-written member in the file.
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("compress record: %w", err)
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("compress record: %w", err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.f.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("write record: %w", err)
	}
	return nil
}

func (w *GzipJSONLWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}
