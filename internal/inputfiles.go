package internal

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// maxInputLine is the longest line ReadQueryRequests accepts.
const maxInputLine = 16 << 20

// RenderInputFiles renders cfg's input file templates with vars, replacing them with the glob
// patterns they produce, and checks that each pattern is valid. The patterns are matched
// against the filesystem by GlobInputFiles each time the input files are polled.
func RenderInputFiles(cfg Config, vars TemplateVars) (Config, error) {
	var errs []error
	patterns := make([]string, 0, len(cfg.InputFiles))
	seen := make(map[string]bool)
	for _, tpl := range cfg.InputFiles {
		pattern, err := RenderTemplate(tpl, vars)
		if err != nil {
			errs = append(errs, fmt.Errorf("input_files: render %q: %w", tpl, err))
			continue
		}
		if pattern == "" {
			errs = append(errs, fmt.Errorf("input_files: %q rendered to an empty path", tpl))
			continue
		}
		if _, err := filepath.Match(pattern, ""); err != nil {
			errs = append(errs, fmt.Errorf("input_files: %q rendered to invalid pattern %q: %w", tpl, pattern, err))
			continue
		}
		if !seen[pattern] {
			seen[pattern] = true
			patterns = append(patterns, pattern)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	cfg.InputFiles = patterns
	return cfg, nil
}

// CheckInputOutputOverlap returns an error naming each input file pattern that matches one
// of outputPaths: the agent would read back its own results as queries. (Output files are
// also skipped when polling, in case a pattern only matches one later, e.g. the next day's.)
func CheckInputOutputOverlap(patterns, outputPaths []string) error {
	var errs []error
	for _, pattern := range patterns {
		absPattern, err := filepath.Abs(pattern)
		if err != nil {
			continue
		}
		for _, out := range outputPaths {
			absOut, err := filepath.Abs(out)
			if err != nil {
				continue
			}
			if ok, _ := filepath.Match(absPattern, absOut); ok {
				errs = append(errs, fmt.Errorf("input_files pattern %q matches output file %q", pattern, out))
			}
		}
	}
	return errors.Join(errs...)
}

// GlobInputFiles returns the files the glob patterns currently match, in the order of the
// patterns and in lexical order within each. A file matched by more than one pattern is
// listed once. Patterns that match nothing are returned in unmatched.
func GlobInputFiles(patterns []string) (paths, unmatched []string, _ error) {
	var errs []error
	seen := make(map[string]bool)
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			errs = append(errs, fmt.Errorf("input_files: invalid pattern %q: %w", pattern, err))
			continue
		}
		if len(matches) == 0 {
			unmatched = append(unmatched, pattern)
		}
		for _, path := range matches {
			if !seen[path] {
				seen[path] = true
				paths = append(paths, path)
			}
		}
	}
	return paths, unmatched, errors.Join(errs...)
}

// isJSONInputFile reports whether the input file at path is a JSON document (named *.json or
// *.json.gz) rather than JSONL (one request per line, the format of any other file).
func isJSONInputFile(path string) bool {
	return strings.HasSuffix(strings.TrimSuffix(strings.ToLower(path), ".gz"), ".json")
}

// gunzipIfCompressed returns a reader of br's contents, decompressing them if they are
// gzip-compressed (including multiple concatenated gzip members), and a function that
// releases the decompressor.
func gunzipIfCompressed(br *bufio.Reader) (io.Reader, func(), error) {
	if magic, _ := br.Peek(2); !bytes.Equal(magic, []byte{0x1f, 0x8b}) {
		return br, func() {}, nil
	}
	zr, err := gzip.NewReader(br)
	if err != nil {
		return nil, nil, fmt.Errorf("open gzip stream: %w", err)
	}
	return zr, func() { zr.Close() }, nil
}

// readJSONQueryRequests reads a JSON input file (gzip-compressed or plain) and returns the
// query requests in it, unparsed, in order. The file holds a request object, an array of
// them, or a sequence of either (so JSONL content can be read too). An element that is not
// an object is still returned, for ParseQueryRequest to reject. If the file is not valid
// JSON, nothing is returned but the error.
func readJSONQueryRequests(r io.Reader) ([]json.RawMessage, error) {
	r, done, err := gunzipIfCompressed(bufio.NewReader(r))
	if err != nil {
		return nil, err
	}
	defer done()

	var raws []json.RawMessage
	dec := json.NewDecoder(r)
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); errors.Is(err, io.EOF) {
			return raws, nil
		} else if err != nil {
			return nil, err
		}
		if raw[0] != '[' {
			raws = append(raws, raw)
			continue
		}
		var elems []json.RawMessage
		if err := json.Unmarshal(raw, &elems); err != nil {
			return nil, err
		}
		raws = append(raws, elems...)
	}
}

// requestSum returns the SHA-256 of a request read from a JSON input file, ignoring
// insignificant whitespace, so that it can be compared with the request read before.
func requestSum(raw json.RawMessage) [sha256.Size]byte {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return sha256.Sum256(raw) // cannot happen: raw was decoded as valid JSON
	}
	return sha256.Sum256(buf.Bytes())
}

// ReadQueryRequests reads query requests from r, one per line, and calls fn with each
// line's 1-based number and the request parsed from it by ParseQueryRequest (or the parse
// error). Blank lines are skipped. The input may be gzip-compressed (including multiple
// concatenated gzip members, as GzipJSONLWriter produces) or plain text.
//
// Reading stops at the first error returned by fn, which ReadQueryRequests then returns.
func ReadQueryRequests(r io.Reader, fn func(line int, req QueryRequest, err error) error) error {
	_, _, err := readQueryRequests(r, 0, true, fn)
	return err
}

// readQueryRequests is ReadQueryRequests for a file that may still be growing. Lines up to
// and including line number after are skipped without being parsed. If the input ends with
// a line that has no trailing newline, which may be a line still being written, it is
// passed to fn only if unterminated is true; otherwise it is left unread and pending is
// true. A partial line left by a read error, such as a truncated gzip member, is never
// passed to fn.
//
// last is the number of the last line read: skipped, blank or passed to fn (including the
// line whose fn call returned an error). Reading the same input again with after = last
// resumes where this call stopped.
func readQueryRequests(r io.Reader, after int, unterminated bool, fn func(line int, req QueryRequest, err error) error) (last int, pending bool, err error) {
	r, done, err := gunzipIfCompressed(bufio.NewReader(r))
	if err != nil {
		return 0, false, err
	}
	defer done()

	src := &errRecorder{r: r}
	terminated := true
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64<<10), maxInputLine)
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		advance, token, err := bufio.ScanLines(data, atEOF)
		if token == nil {
			return advance, token, err
		}
		terminated = data[advance-1] == '\n'
		if !terminated && (src.err != nil || !unterminated) {
			pending = src.err == nil
			return 0, nil, nil // leave the partial line unread
		}
		return advance, token, err
	})

	for scanner.Scan() {
		last++
		if last <= after {
			continue
		}
		data := bytes.TrimSpace(scanner.Bytes())
		if len(data) == 0 {
			continue
		}
		req, err := ParseQueryRequest(data)
		if err := fn(last, req, err); err != nil {
			return last, false, err
		}
	}
	if err := scanner.Err(); err != nil {
		return last, false, fmt.Errorf("line %d: %w", last+1, err)
	}
	if last < after {
		// The input is shorter than it was when it was last read.
		return last, false, fmt.Errorf("has %d lines, fewer than the %d already read", last, after)
	}
	return last, pending, nil
}

// errRecorder records the first error other than io.EOF returned by reads from r.
type errRecorder struct {
	r   io.Reader
	err error
}

func (e *errRecorder) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && err != io.EOF && e.err == nil {
		e.err = err
	}
	return n, err
}
