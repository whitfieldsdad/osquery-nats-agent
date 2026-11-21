package internal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseQueryRequest(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    QueryRequest
		wantErr bool
	}{
		{
			name:    "plain SQL is rejected",
			in:      "SELECT name, version FROM os_version;",
			wantErr: true,
		},
		{
			name:    "JSON string is rejected",
			in:      `"SELECT 1;"`,
			wantErr: true,
		},
		{
			name: "multi-line query",
			in:   `{"query": "SELECT pid,\n       name\nFROM processes;"}`,
			want: QueryRequest{Query: "SELECT pid,\n       name\nFROM processes;"},
		},
		{
			name: "JSON request",
			in:   `{"query": " SELECT 1; ", "platforms": ["linux"]}`,
			want: QueryRequest{Query: "SELECT 1;", Platforms: []string{"linux"}},
		},
		{
			name: "JSON request with leading whitespace",
			in:   "  \n{\"query\": \"SELECT 1;\"}",
			want: QueryRequest{Query: "SELECT 1;"},
		},
		{
			name:    "malformed JSON",
			in:      `{"query": "SELECT 1;"`,
			wantErr: true,
		},
		{
			name:    "empty message",
			in:      "   ",
			wantErr: true,
		},
		{
			name: "no query",
			in:   `{"platforms": ["linux"]}`,
			want: QueryRequest{Platforms: []string{"linux"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseQueryRequest([]byte(tt.in))
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseQueryRequest() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseQueryRequest() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestNewResultIncludesDateAndTime(t *testing.T) {
	// 07:34:56 in UTC-5 is 12:34:56 UTC.
	finished := time.Date(2026, 11, 20, 7, 34, 56, 789, time.FixedZone("EST", -5*60*60))
	a := &Agent{now: func() time.Time { return finished }}

	result := a.newResult("", "SELECT 1;", nil, nil)
	if result.Date != "2026-11-20" {
		t.Errorf("Date = %q, want %q", result.Date, "2026-11-20")
	}
	if result.Time != "2026-11-20T12:34:56" {
		t.Errorf("Time = %q, want %q", result.Time, "2026-11-20T12:34:56")
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["date"] != "2026-11-20" || fields["time"] != "2026-11-20T12:34:56" {
		t.Errorf("encoded date/time = %v/%v", fields["date"], fields["time"])
	}
}

// lineRecorder is a slog.Handler that records the line numbers (or, for a JSON file, the
// request numbers) of the input file queries the agent skipped for targeting another
// platform, i.e. the queries it read.
type lineRecorder struct {
	mu    sync.Mutex
	lines []int64
}

func (r *lineRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *lineRecorder) WithAttrs([]slog.Attr) slog.Handler       { return r }
func (r *lineRecorder) WithGroup(string) slog.Handler            { return r }
func (r *lineRecorder) Handle(_ context.Context, rec slog.Record) error {
	if rec.Message != "ignoring query for other platforms" {
		return nil
	}
	rec.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "line" || attr.Key == "request" {
			r.mu.Lock()
			r.lines = append(r.lines, attr.Value.Int64())
			r.mu.Unlock()
		}
		return true
	})
	return nil
}

// take returns the lines recorded since it was last called.
func (r *lineRecorder) take() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	lines := r.lines
	r.lines = nil
	return lines
}

func TestPollInputFilesOnce(t *testing.T) {
	// The queries target another platform so that they are read but not run.
	q := func(n int) string {
		return fmt.Sprintf(`{"query": "SELECT %d;", "platforms": ["no-such-platform"]}`+"\n", n)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "a.jsonl")
	rec := &lineRecorder{}
	a := &Agent{
		cfg: Config{InputFiles: []string{filepath.Join(dir, "*.jsonl")}},
		log: slog.New(rec),
	}
	files := make(map[string]*inputFile)
	unmatched := make(map[string]bool)
	// mtime is advanced explicitly on each write so that changes are seen even on
	// filesystems with coarse timestamps.
	mtime := time.Now().Add(-time.Hour)
	touch := func() {
		t.Helper()
		mtime = mtime.Add(time.Second)
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	write := func(data string, flag int) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|flag, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(data); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		touch()
	}
	poll := func(wantLines ...int64) {
		t.Helper()
		a.pollInputFilesOnce(context.Background(), files, unmatched)
		if got := rec.take(); !reflect.DeepEqual(got, wantLines) {
			t.Errorf("poll read lines %v, want %v", got, wantLines)
		}
	}
	check := func(wantLine int, wantPending bool) {
		t.Helper()
		f := files[path]
		if f == nil {
			t.Fatal("file is not tracked")
		}
		if f.line != wantLine || f.pending != wantPending {
			t.Errorf("line %d, pending %v; want %d, %v", f.line, f.pending, wantLine, wantPending)
		}
	}

	poll()
	if !unmatched[a.cfg.InputFiles[0]] || len(files) != 0 {
		t.Fatalf("unmatched = %v, files = %v; want the pattern unmatched and no files", unmatched, files)
	}

	write(q(1)+q(2), os.O_TRUNC)
	poll(1, 2)
	if len(unmatched) != 0 {
		t.Errorf("unmatched = %v after a file appeared", unmatched)
	}
	check(2, false)

	// Unchanged, or only touched: nothing is read again.
	poll()
	touch()
	poll()
	check(2, false)

	// Appended lines are read; a final line without a newline waits for a poll in which the
	// file does not change.
	write(q(3)+`{"query": "SELECT`, os.O_APPEND)
	poll(3)
	check(3, true)
	write(` 4;", "platforms": ["no-such-platform"]}`, os.O_APPEND)
	poll()
	check(3, true)
	poll(4)
	check(4, false)

	// Rewritten in place with different content, as the same file and no smaller: the
	// already-read bytes have changed, so it is read from the start.
	write(q(5)+q(6)+q(7)+q(8)+q(9), os.O_TRUNC)
	poll(1, 2, 3, 4, 5)
	check(5, false)

	// Rewritten in place with different content of exactly the same size.
	write(q(1)+q(2)+q(3)+q(4)+q(5), os.O_TRUNC)
	poll(1, 2, 3, 4, 5)

	// Rewritten in place with the same content plus more: an append.
	write(q(1)+q(2)+q(3)+q(4)+q(5)+q(6), os.O_TRUNC)
	poll(6)
	check(6, false)

	// Replaced by a different file: read from the start.
	tmp := filepath.Join(dir, "a.tmp")
	if err := os.WriteFile(tmp, []byte(q(1)+q(2)+q(3)+q(4)+q(5)+q(6)+q(7)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	touch()
	poll(1, 2, 3, 4, 5, 6, 7)
	check(7, false)

	// Truncated: read from the start.
	write(q(1), os.O_TRUNC)
	poll(1)
	check(1, false)

	// No longer matched: forgotten.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	poll()
	if len(files) != 0 {
		t.Errorf("files = %v after the only file was removed", files)
	}
}

func TestPollJSONInputFile(t *testing.T) {
	// The queries target another platform so that they are read but not run.
	q := func(n int) string {
		return fmt.Sprintf(`{"query": "SELECT %d;", "platforms": ["no-such-platform"]}`, n)
	}
	array := func(ns ...int) string {
		elems := make([]string, len(ns))
		for i, n := range ns {
			elems[i] = q(n)
		}
		return "[" + strings.Join(elems, ",") + "]"
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "queries.json")
	rec := &lineRecorder{}
	a := &Agent{
		cfg: Config{InputFiles: []string{filepath.Join(dir, "*.json")}},
		log: slog.New(rec),
	}
	files := make(map[string]*inputFile)
	unmatched := make(map[string]bool)
	mtime := time.Now().Add(-time.Hour)
	write := func(data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		mtime = mtime.Add(time.Second)
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	poll := func(wantRequests ...int64) {
		t.Helper()
		a.pollInputFilesOnce(context.Background(), files, unmatched)
		if got := rec.take(); !reflect.DeepEqual(got, wantRequests) {
			t.Errorf("poll read requests %v, want %v", got, wantRequests)
		}
	}

	write(array(1, 2))
	poll(1, 2)
	poll() // unchanged

	// The same requests, reformatted: nothing new.
	write("[\n  " + q(1) + ",\n  " + q(2) + "\n]\n")
	poll()

	// A request added to the end runs; removing it again runs nothing.
	write(array(1, 2, 3))
	poll(3)
	write(array(1, 2))
	poll()

	// A changed request: the file was rewritten, so every request runs.
	write(array(5, 2))
	poll(1, 2)

	// Invalid JSON, e.g. a file still being written: nothing runs, and what was read before
	// is remembered for when it is complete.
	write("[" + q(5) + "," + q(2) + ",")
	poll()
	poll()
	write(array(5, 2, 3))
	poll(3)

	// Replaced by a new file with the same requests plus another: only the new one runs.
	tmp := filepath.Join(dir, "queries.tmp")
	if err := os.WriteFile(tmp, []byte(array(5, 2, 3, 4)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	poll(4)

	// A single request object and a stream of them (JSONL content) are read too, as is
	// gzip-compressed JSON.
	write(q(9))
	poll(1)
	write(q(9) + "\n" + q(10) + "\n")
	poll(2)
	write(string(gzipBytes(t, array(9, 10, 11))))
	poll(3)

	// Cut down to its first request: a removal, so nothing runs.
	write(q(9))
	poll()
}

// summaryRecorder is a slog.Handler that records the "changes" summaries of successful
// queries.
type summaryRecorder struct {
	mu        sync.Mutex
	summaries []string
}

func (r *summaryRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *summaryRecorder) WithAttrs([]slog.Attr) slog.Handler       { return r }
func (r *summaryRecorder) WithGroup(string) slog.Handler            { return r }
func (r *summaryRecorder) Handle(_ context.Context, rec slog.Record) error {
	rec.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "changes" {
			r.mu.Lock()
			r.summaries = append(r.summaries, attr.Value.String())
			r.mu.Unlock()
		}
		return true
	})
	return nil
}

func TestQueriesAreDifferential(t *testing.T) {
	row := func(pid int, name string) map[string]any { return map[string]any{"pid": float64(pid), "name": name} }
	// What each run of each query returns, in order. An error is a failed run.
	runs := map[string][]any{
		"SELECT * FROM processes;": {
			[]map[string]any{row(1, "init"), row(2, "sshd"), row(3, "bash"), row(4, "vim")},
			[]map[string]any{row(1, "init"), row(2, "sshd"), row(3, "bash"), row(4, "vim")},
			errors.New("osqueryi crashed"),
			[]map[string]any{row(1, "init"), row(2, "sshd"), row(3, "zsh"), row(5, "top")},
		},
		"SELECT 1;": {
			[]map[string]any{{"1": "1"}},
		},
	}
	var mu sync.Mutex
	path := filepath.Join(t.TempDir(), "results.jsonl.gz")
	out, err := OpenGzipJSONLs(func(time.Time) (string, error) { return path, nil })
	if err != nil {
		t.Fatal(err)
	}
	rec := &summaryRecorder{}
	a := &Agent{
		cfg: Config{QueryTimeout: Duration(time.Minute)},
		out: out,
		log: slog.New(rec),
		now: time.Now,
		query: func(_ context.Context, query string) ([]map[string]any, error) {
			mu.Lock()
			defer mu.Unlock()
			next := runs[query][0]
			runs[query] = runs[query][1:]
			if err, ok := next.(error); ok {
				return nil, err
			}
			return next.([]map[string]any), nil
		},
	}

	// Queries received over NATS or read from input files share a baseline per query; a
	// scheduled query has its own.
	ps := QueryRequest{Query: "SELECT * FROM processes;"}
	a.runRequest(ps)
	a.runRequest(QueryRequest{Query: "SELECT 1;"})
	a.runRequest(ps) // no changes: nothing delivered
	a.runRequest(ps) // fails: an error is delivered and the baseline is kept
	a.runRequest(ps) // bash -> zsh, vim removed, top added
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}

	type delivered struct {
		Query   string           `json:"query"`
		Error   *string          `json:"error"`
		Results []map[string]any `json:"results"`
		Removed []map[string]any `json:"removed"`
	}
	var got []delivered
	for _, line := range readGzipJSONL(t, path) {
		var d delivered
		if err := json.Unmarshal([]byte(line), &d); err != nil {
			t.Fatal(err)
		}
		got = append(got, d)
	}
	want := []delivered{
		{Query: ps.Query, Results: []map[string]any{row(1, "init"), row(2, "sshd"), row(3, "bash"), row(4, "vim")}},
		{Query: "SELECT 1;", Results: []map[string]any{{"1": "1"}}},
		{Query: ps.Query, Error: ptr("osqueryi crashed")},
		{
			Query:   ps.Query,
			Results: []map[string]any{row(3, "zsh"), row(5, "top")},
			Removed: []map[string]any{row(3, "bash"), row(4, "vim")},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("delivered:\n%+v\nwant:\n%+v", got, want)
	}

	wantSummaries := []string{
		"4/4 rows changed (100%)",
		"1/1 rows changed (100%)",
		"0/4 rows changed (0%)",
		"4/6 rows changed (67%)", // 2 added + 2 removed, 2 unchanged
	}
	if !reflect.DeepEqual(rec.summaries, wantSummaries) {
		t.Errorf("summaries = %q, want %q", rec.summaries, wantSummaries)
	}
}

func TestSnapshotQueries(t *testing.T) {
	row := func(pid int) map[string]any { return map[string]any{"pid": float64(pid)} }
	// What each run of each query returns, in order. An error is a failed run.
	runs := map[string][]any{
		"SELECT pid FROM processes;": {
			[]map[string]any{row(1), row(2)},
			[]map[string]any{row(1), row(2)}, // unchanged: delivered in full again
			errors.New("osqueryi crashed"),
			[]map[string]any{row(2), row(3)},
			[]map[string]any(nil), // no rows: delivered as an empty result
		},
		"SELECT 1;": {
			[]map[string]any{{"1": "1"}},
		},
	}
	var mu sync.Mutex
	path := filepath.Join(t.TempDir(), "results.jsonl.gz")
	out, err := OpenGzipJSONLs(func(time.Time) (string, error) { return path, nil })
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{
		cfg: Config{QueryTimeout: Duration(time.Minute), Snapshot: true},
		out: out,
		log: slog.New(slog.DiscardHandler),
		now: time.Now,
		query: func(_ context.Context, query string) ([]map[string]any, error) {
			mu.Lock()
			defer mu.Unlock()
			next := runs[query][0]
			runs[query] = runs[query][1:]
			if err, ok := next.(error); ok {
				return nil, err
			}
			return next.([]map[string]any), nil
		},
	}

	ps := QueryRequest{Query: "SELECT pid FROM processes;"}
	for range 5 {
		a.runRequest(ps)
	}
	// A scheduled query runs once straight away; with ctx already cancelled, it then stops.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.runSchedule(ctx, ScheduledQuery{Name: "one", Query: "SELECT 1;", Interval: 60})
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}

	var got []QueryResult
	for _, line := range readGzipJSONL(t, path) {
		var r QueryResult
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(line, `"results":`) || strings.Contains(line, `"removed"`) {
			t.Errorf("result %s: want a results list and no removed list", line)
		}
		got = append(got, QueryResult{Name: r.Name, Query: r.Query, Error: r.Error, Results: r.Results, Removed: r.Removed})
	}
	want := []QueryResult{
		{Query: ps.Query, Results: []map[string]any{row(1), row(2)}},
		{Query: ps.Query, Results: []map[string]any{row(1), row(2)}},
		{Query: ps.Query, Error: ptr("osqueryi crashed")},
		{Query: ps.Query, Results: []map[string]any{row(2), row(3)}},
		{Query: ps.Query, Results: []map[string]any{}},
		{Name: "one", Query: "SELECT 1;", Results: []map[string]any{{"1": "1"}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("delivered:\n%+v\nwant:\n%+v", got, want)
	}
	if len(a.baselines) != 0 {
		t.Errorf("%d baselines kept in snapshot mode, want none", len(a.baselines))
	}
}

func TestHashPrefixes(t *testing.T) {
	data := []byte("SELECT 1;\nSELECT 2;\n")
	nSum, sizeSum, err := hashPrefixes(bytes.NewReader(data), 10, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if want := sha256.Sum256(data[:10]); !bytes.Equal(nSum, want[:]) {
		t.Errorf("sum of first 10 bytes = %x, want %x", nSum, want)
	}
	if want := sha256.Sum256(data); !bytes.Equal(sizeSum, want[:]) {
		t.Errorf("sum of all bytes = %x, want %x", sizeSum, want)
	}
	if _, _, err := hashPrefixes(bytes.NewReader(data), 10, int64(len(data))+1); err == nil {
		t.Error("expected an error when the input is shorter than size")
	}
}

func TestEncodeForNATS(t *testing.T) {
	result := QueryResult{Query: "SELECT 1;", Results: []map[string]any{{"n": 1}}}
	msgs, dropped, err := encodeForNATS(result, []string{"a.jsonl.gz"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := json.Marshal(result); len(msgs) != 1 || string(msgs[0]) != string(want) || dropped != 0 {
		t.Errorf("small result = %q, %d dropped; want %s", msgs, dropped, want)
	}

	// Too big for one message: split, with every row in exactly one message.
	big := QueryResult{Query: "SELECT * FROM processes;", Results: []map[string]any{}}
	for i := range 50 {
		big.Results = append(big.Results, map[string]any{"pid": float64(i), "name": strings.Repeat("x", 40)})
	}
	for i := range 30 {
		big.Removed = append(big.Removed, map[string]any{"pid": float64(100 + i), "name": "old"})
	}
	const limit = 400
	msgs, dropped, err = encodeForNATS(big, nil, limit)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) < 2 || dropped != 0 {
		t.Fatalf("got %d messages, %d dropped; want several, none dropped", len(msgs), dropped)
	}
	var results, removed []map[string]any
	for _, m := range msgs {
		if len(m) > limit {
			t.Errorf("message is %d bytes, over the %d byte limit", len(m), limit)
		}
		var got QueryResult
		if err := json.Unmarshal(m, &got); err != nil {
			t.Fatal(err)
		}
		if got.Query != big.Query || got.Error != nil || got.Results == nil {
			t.Errorf("message = %s, want the query, no error and a results list", m)
		}
		results, removed = append(results, got.Results...), append(removed, got.Removed...)
	}
	if !reflect.DeepEqual(results, big.Results) || !reflect.DeepEqual(removed, big.Removed) {
		t.Errorf("rows across messages differ from the result's:\nresults %v\nremoved %v", results, removed)
	}

	// A row too big for a message on its own is replaced by an error.
	huge := QueryResult{Query: "SELECT 1;", Results: []map[string]any{{"n": 1}, {"blob": strings.Repeat("y", 1000)}}}
	msgs, dropped, err = encodeForNATS(huge, []string{"a.jsonl.gz", "b.jsonl.gz"}, 300)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || dropped != 1 {
		t.Fatalf("got %q, %d dropped; want 2 messages, 1 dropped", msgs, dropped)
	}
	var kept, stub QueryResult
	if err := json.Unmarshal(msgs[0], &kept); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(msgs[1], &stub); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(kept.Results, []map[string]any{{"n": float64(1)}}) || kept.Error != nil {
		t.Errorf("first message = %s, want the small row", msgs[0])
	}
	if stub.Results != nil || stub.Error == nil ||
		!strings.Contains(*stub.Error, "full result written to a.jsonl.gz, b.jsonl.gz on the host") {
		t.Errorf("second message = %s, want an error naming the output files", msgs[1])
	}
}

func TestUndeliveredChangesAreDeliveredAgain(t *testing.T) {
	row := func(pid int) map[string]any { return map[string]any{"pid": float64(pid)} }
	runs := [][]map[string]any{
		{row(1)},
		{row(1), row(2)}, // not delivered: the output file cannot be written
		{row(1), row(2), row(3)},
	}
	path := filepath.Join(t.TempDir(), "results.jsonl.gz")
	var failWrites bool
	out, err := OpenGzipJSONLs(func(time.Time) (string, error) {
		if failWrites {
			return "", errors.New("disk full")
		}
		return path, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{
		cfg: Config{QueryTimeout: Duration(time.Minute)},
		out: out,
		log: slog.New(slog.DiscardHandler),
		now: time.Now,
		query: func(context.Context, string) ([]map[string]any, error) {
			next := runs[0]
			runs = runs[1:]
			return next, nil
		},
	}
	var b baseline
	a.runDiff(&b, "", "SELECT pid FROM processes;", a.log)
	failWrites = true
	a.runDiff(&b, "", "SELECT pid FROM processes;", a.log)
	failWrites = false
	a.runDiff(&b, "", "SELECT pid FROM processes;", a.log)
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}

	var got [][]map[string]any
	for _, line := range readGzipJSONL(t, path) {
		var r QueryResult
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		got = append(got, r.Results)
	}
	// The third run reports the second run's undelivered change (pid 2) along with its own.
	want := [][]map[string]any{{row(1)}, {row(2), row(3)}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("delivered %v, want %v", got, want)
	}
}

func TestBaselinesAreBounded(t *testing.T) {
	const limit = 3
	a := &Agent{cfg: Config{MaxCachedQueries: limit}}
	first := a.baselineFor("SELECT 0;")
	for i := 1; i < limit; i++ {
		a.baselineFor(fmt.Sprintf("SELECT %d;", i))
	}
	if a.baselineFor("SELECT 0;") != first {
		t.Fatal("baseline was dropped before the limit was reached")
	}
	// SELECT 0 was just used, so adding one more drops SELECT 1, the least recently used.
	a.baselineFor("SELECT new;")
	if len(a.baselines) != limit || a.baselineLRU.Len() != limit {
		t.Errorf("%d baselines (%d in the LRU list), want %d", len(a.baselines), a.baselineLRU.Len(), limit)
	}
	if a.baselines["SELECT 1;"] != nil || a.baselines["SELECT 0;"] == nil {
		t.Error("the wrong baseline was dropped")
	}
}

func TestPollSkipsOutputFiles(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "queries.jsonl")
	results := filepath.Join(dir, "results.jsonl")
	query := `{"query": "SELECT 1;", "platforms": ["no-such-platform"]}` + "\n"
	for _, path := range []string{in, results} {
		if err := os.WriteFile(path, []byte(query), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rec := &lineRecorder{}
	a := &Agent{cfg: Config{InputFiles: []string{filepath.Join(dir, "*.jsonl")}}, log: slog.New(rec)}
	a.rememberOutputPaths([]string{results})

	files := make(map[string]*inputFile)
	a.pollInputFilesOnce(context.Background(), files, make(map[string]bool))
	if got := rec.take(); !reflect.DeepEqual(got, []int64{1}) {
		t.Errorf("read lines %v, want only line 1 of the input file", got)
	}
	if f := files[results]; f == nil || !f.output {
		t.Errorf("output file state = %+v, want it tracked as an output", f)
	}
}

