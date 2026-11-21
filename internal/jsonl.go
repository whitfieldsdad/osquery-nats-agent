package internal

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// GzipJSONLWriter appends JSON records, one per line, to gzip-compressed files. The file
// written to is chosen per record by a path function of the current time, so results can be
// partitioned by date: when the path changes, the old file is closed and the new one opened.
//
// Each record is written as its own complete gzip member. Concatenated gzip members form a
// valid gzip stream, so files stay readable with zcat, gzip.NewReader, etc. after every
// write and survive the agent being killed or restarted (files are opened in append mode).
type GzipJSONLWriter struct {
	pathAt func(time.Time) (string, error)
	now    func() time.Time

	mu   sync.Mutex
	path string
	f    *os.File
}

// OpenGzipJSONL returns a writer that appends to the file pathAt returns for the current
// time. It opens the current file immediately, so a bad path is reported at startup.
func OpenGzipJSONL(pathAt func(time.Time) (string, error)) (*GzipJSONLWriter, error) {
	w := &GzipJSONLWriter{pathAt: pathAt, now: time.Now}
	if _, err := w.currentFile(); err != nil {
		return nil, err
	}
	return w, nil
}

// Path returns the file most recently written to (or opened).
func (w *GzipJSONLWriter) Path() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.path
}

// Write appends v as a single JSON line and returns the path of the file it was written to.
func (w *GzipJSONLWriter) Write(v any) (string, error) {
	member, err := encodeGzipJSONLRecord(v)
	if err != nil {
		return "", err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	f, err := w.currentFile()
	if err != nil {
		return "", err
	}
	return w.writeMember(f, member)
}

// writeMember appends an encoded record to f, the current file. The caller must hold w.mu.
func (w *GzipJSONLWriter) writeMember(f *os.File, member []byte) (string, error) {
	if _, err := f.Write(member); err != nil {
		return w.path, fmt.Errorf("write record to %s: %w", w.path, err)
	}
	return w.path, nil
}

// encodeGzipJSONLRecord encodes v as a JSON line compressed into a complete gzip member.
// Compressing into memory first means a failure can't leave a half-written member in a file.
func encodeGzipJSONLRecord(v any) ([]byte, error) {
	line, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode record: %w", err)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(append(line, '\n')); err != nil {
		return nil, fmt.Errorf("compress record: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("compress record: %w", err)
	}
	return buf.Bytes(), nil
}

// currentFile returns the file for the current time, switching files if the path changed.
// The caller must hold w.mu (or have exclusive access).
func (w *GzipJSONLWriter) currentFile() (*os.File, error) {
	path, err := w.pathAt(w.now())
	if err != nil {
		return nil, err
	}
	if w.f != nil && path == w.path {
		return w.f, nil
	}

	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create output directory: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open output file: %w", err)
	}
	if w.f != nil {
		w.f.Close() // every record is already a complete gzip member; nothing to flush
	}
	w.f, w.path = f, path
	return f, nil
}

func (w *GzipJSONLWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// GzipJSONLWriters appends every record to each of several GzipJSONLWriters.
type GzipJSONLWriters []*GzipJSONLWriter

// OpenGzipJSONLs opens a GzipJSONLWriter for each path function, closing those already
// opened if one fails.
func OpenGzipJSONLs(pathAts ...func(time.Time) (string, error)) (GzipJSONLWriters, error) {
	ws := make(GzipJSONLWriters, 0, len(pathAts))
	for _, pathAt := range pathAts {
		w, err := OpenGzipJSONL(pathAt)
		if err != nil {
			ws.Close()
			return nil, err
		}
		ws = append(ws, w)
	}
	return ws, nil
}

// Write appends v as a single JSON line to the current file of every writer, compressing it
// only once, and returns the paths of the files it was written to. If two writers' current
// files have the same path, the record is written to it once. A failure to write to one
// file does not stop the record being written to the others; the errors are joined.
func (ws GzipJSONLWriters) Write(v any) (paths []string, _ error) {
	if len(ws) == 0 {
		return nil, nil
	}
	member, err := encodeGzipJSONLRecord(v)
	if err != nil {
		return nil, err
	}
	var errs []error
	written := make(map[string]bool, len(ws))
	for _, w := range ws {
		path, err := w.writeIfNew(member, written)
		if err != nil {
			errs = append(errs, err)
		} else if path != "" {
			paths = append(paths, path)
		}
	}
	return paths, errors.Join(errs...)
}

// writeIfNew appends member to w's current file unless its path is already in written, and
// adds the path to written. It returns the path written to, or "" if it was skipped.
func (w *GzipJSONLWriter) writeIfNew(member []byte, written map[string]bool) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	f, err := w.currentFile()
	if err != nil {
		return "", err
	}
	if written[w.path] {
		return "", nil
	}
	written[w.path] = true
	return w.writeMember(f, member)
}

// Paths returns the file most recently written to (or opened) by each writer.
func (ws GzipJSONLWriters) Paths() []string {
	paths := make([]string, len(ws))
	for i, w := range ws {
		paths[i] = w.Path()
	}
	return paths
}

// Close closes every writer, returning their errors joined.
func (ws GzipJSONLWriters) Close() error {
	var errs []error
	for _, w := range ws {
		errs = append(errs, w.Close())
	}
	return errors.Join(errs...)
}
