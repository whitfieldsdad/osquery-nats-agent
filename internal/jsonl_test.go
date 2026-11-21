package internal

import (
	"bufio"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func readGzipJSONL(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	sc := bufio.NewScanner(zr)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return lines
}

func TestGzipJSONLWriterPartitionsByDate(t *testing.T) {
	dir := t.TempDir()
	tpl, err := NewOutputPathTemplate(filepath.Join(dir, "data/{{ date }}/{{ hostname }}.jsonl.gz"), TemplateVars{Hostname: "lab-linux-1"})
	if err != nil {
		t.Fatal(err)
	}

	// 23:59 local time in UTC-5 is already the next day in UTC.
	est := time.FixedZone("EST", -5*3600)
	clock := time.Date(2026, 11, 20, 18, 0, 0, 0, est) // 23:00 UTC on the 20th
	w := &GzipJSONLWriter{pathAt: tpl.PathAt, now: func() time.Time { return clock }}
	defer w.Close()

	write := func(v any) string {
		t.Helper()
		path, err := w.Write(v)
		if err != nil {
			t.Fatal(err)
		}
		return path
	}

	day1 := filepath.Join(dir, "data/2026-11-20/lab-linux-1.jsonl.gz")
	if got := write(map[string]int{"n": 1}); got != day1 {
		t.Errorf("path = %s, want %s", got, day1)
	}
	write(map[string]int{"n": 2})

	clock = time.Date(2026, 11, 20, 19, 0, 0, 0, est) // 00:00 UTC on the 21st
	day2 := filepath.Join(dir, "data/2026-11-21/lab-linux-1.jsonl.gz")
	if got := write(map[string]int{"n": 3}); got != day2 {
		t.Errorf("path = %s, want %s", got, day2)
	}

	if got, want := readGzipJSONL(t, day1), []string{`{"n":1}`, `{"n":2}`}; !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %q, want %q", day1, got, want)
	}
	if got, want := readGzipJSONL(t, day2), []string{`{"n":3}`}; !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %q, want %q", day2, got, want)
	}
}

func TestGzipJSONLWriterAppendsAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.jsonl.gz")
	pathAt := func(time.Time) (string, error) { return path, nil }
	for i := range 2 {
		w, err := OpenGzipJSONL(pathAt)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(map[string]int{"run": i}); err != nil {
			t.Fatal(err)
		}
		w.Close()
	}
	if got, want := readGzipJSONL(t, path), []string{`{"run":0}`, `{"run":1}`}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestOutputPathTemplateErrors(t *testing.T) {
	if _, err := NewOutputPathTemplate("data/{{ date", TemplateVars{}); err == nil {
		t.Error("unterminated template parsed without error")
	}
	tpl, err := NewOutputPathTemplate("data/{{ ip_address }}/out.jsonl.gz", TemplateVars{Hostname: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tpl.PathAt(time.Now()); err == nil {
		t.Error("rendering an unknown ip_address succeeded, want an error")
	}
}

func TestOutputPathTemplateHourly(t *testing.T) {
	tpl, err := NewOutputPathTemplate("data/{{ date }}-{{ clock_time[:2] }}/osquery-results.jsonl.gz", TemplateVars{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		t    time.Time
		want string
	}{
		{time.Date(2024, 6, 12, 15, 0, 0, 0, time.UTC), "data/2024-06-12-15/osquery-results.jsonl.gz"},
		{time.Date(2024, 6, 12, 15, 59, 59, 0, time.UTC), "data/2024-06-12-15/osquery-results.jsonl.gz"},
		{time.Date(2024, 6, 12, 16, 0, 0, 0, time.UTC), "data/2024-06-12-16/osquery-results.jsonl.gz"},
		{time.Date(2024, 6, 12, 23, 30, 0, 0, time.FixedZone("EST", -5*3600)), "data/2024-06-13-04/osquery-results.jsonl.gz"},
		{time.Date(2024, 6, 12, 3, 0, 0, 0, time.UTC), "data/2024-06-12-03/osquery-results.jsonl.gz"}, // going back re-renders
	} {
		got, err := tpl.PathAt(tc.t)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("PathAt(%s) = %s, want %s", tc.t, got, tc.want)
		}
	}
}

func TestOutputPathTemplateTimeVariables(t *testing.T) {
	tpl, err := NewOutputPathTemplate("{{ date }}|{{ time }}|{{ clock_time }}|{{ time[:13] }}", TemplateVars{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		t    time.Time
		want string
	}{
		{time.Date(2026, 11, 20, 12, 34, 56, 0, time.UTC), "2026-11-20|2026-11-20T12:34:56|12:34:56|2026-11-20T12"},
		{time.Date(2026, 11, 20, 12, 34, 56, 999_000_000, time.UTC), "2026-11-20|2026-11-20T12:34:56|12:34:56|2026-11-20T12"},
		{time.Date(2026, 11, 20, 12, 34, 57, 0, time.UTC), "2026-11-20|2026-11-20T12:34:57|12:34:57|2026-11-20T12"},
		{time.Date(2026, 11, 20, 20, 55, 0, 0, time.FixedZone("EST", -5*3600)), "2026-11-21|2026-11-21T01:55:00|01:55:00|2026-11-21T01"},
	} {
		got, err := tpl.PathAt(tc.t)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("PathAt(%s) = %s, want %s", tc.t, got, tc.want)
		}
	}
}

func TestGzipJSONLWritersAppendToEveryFile(t *testing.T) {
	dir := t.TempDir()
	fixed := func(name string) func(time.Time) (string, error) {
		path := filepath.Join(dir, name)
		return func(time.Time) (string, error) { return path, nil }
	}
	// The third writer renders to the same path as the first, so it is skipped.
	ws, err := OpenGzipJSONLs(fixed("a/results.jsonl.gz"), fixed("b/results.jsonl.gz"), fixed("a/results.jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	for n := range 2 {
		paths, err := ws.Write(map[string]int{"n": n})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{filepath.Join(dir, "a/results.jsonl.gz"), filepath.Join(dir, "b/results.jsonl.gz")}
		if !reflect.DeepEqual(paths, want) {
			t.Errorf("paths = %q, want %q", paths, want)
		}
	}
	if err := ws.Close(); err != nil {
		t.Fatal(err)
	}

	want := []string{`{"n":0}`, `{"n":1}`}
	for _, name := range []string{"a/results.jsonl.gz", "b/results.jsonl.gz"} {
		if got := readGzipJSONL(t, filepath.Join(dir, name)); !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestGzipJSONLWritersKeepWritingAfterAFailure(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.jsonl.gz")
	fail := false
	ws, err := OpenGzipJSONLs(
		func(time.Time) (string, error) {
			if fail {
				return "", errors.New("render failed")
			}
			return filepath.Join(dir, "bad.jsonl.gz"), nil
		},
		func(time.Time) (string, error) { return good, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	fail = true
	paths, err := ws.Write(map[string]int{"n": 1})
	if err == nil || !strings.Contains(err.Error(), "render failed") {
		t.Errorf("err = %v, want the failing writer's error", err)
	}
	if want := []string{good}; !reflect.DeepEqual(paths, want) {
		t.Errorf("paths = %q, want %q", paths, want)
	}
	if err := ws.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readGzipJSONL(t, good); !reflect.DeepEqual(got, []string{`{"n":1}`}) {
		t.Errorf("good file = %q", got)
	}
}

func TestOpenGzipJSONLsReportsBadPath(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := OpenGzipJSONLs(
		func(time.Time) (string, error) { return filepath.Join(dir, "ok.jsonl.gz"), nil },
		func(time.Time) (string, error) { return filepath.Join(blocker, "under-a-file.jsonl.gz"), nil },
	)
	if err == nil {
		t.Fatal("expected an error for an output path under a regular file")
	}
}
