package internal

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type readLine struct {
	Line int
	Req  QueryRequest
	Err  bool
}

func readAll(t *testing.T, data []byte) ([]readLine, error) {
	t.Helper()
	var got []readLine
	err := ReadQueryRequests(bytes.NewReader(data), func(line int, req QueryRequest, err error) error {
		got = append(got, readLine{Line: line, Req: req, Err: err != nil})
		return nil
	})
	return got, err
}

func gzipBytes(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const inputLines = `{"query": "SELECT * FROM uptime;", "platforms": ["linux"]}

SELECT name, version FROM os_version;
{"query": "SELECT 1;"
  {"query": " SELECT 2; "}
`

var wantInputLines = []readLine{
	{Line: 1, Req: QueryRequest{Query: "SELECT * FROM uptime;", Platforms: []string{"linux"}}},
	{Line: 3, Err: true}, // plain SQL is not a query request
	{Line: 4, Err: true},
	{Line: 5, Req: QueryRequest{Query: "SELECT 2;"}},
}

func TestReadQueryRequests(t *testing.T) {
	for name, data := range map[string][]byte{
		"plain": []byte(inputLines),
		"gzip":  gzipBytes(t, inputLines),
		// Separate gzip members, as GzipJSONLWriter writes, concatenated into one stream.
		"gzip members": func() []byte {
			var out []byte
			for _, line := range strings.SplitAfter(inputLines, "\n") {
				out = append(out, gzipBytes(t, line)...)
			}
			return out
		}(),
		"no trailing newline": []byte(strings.TrimSuffix(inputLines, "\n")),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := readAll(t, data)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, wantInputLines) {
				t.Errorf("got %+v, want %+v", got, wantInputLines)
			}
		})
	}
}

func TestReadQueryRequestsReadsGzipJSONLWriterOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queries.jsonl.gz")
	w, err := OpenGzipJSONL(func(time.Time) (string, error) { return path, nil })
	if err != nil {
		t.Fatal(err)
	}
	want := []QueryRequest{
		{Query: "SELECT\n    pid,\n    \"name\"\nFROM processes;", Platforms: []string{"linux", "darwin"}},
		{Query: "SELECT * FROM uptime;"},
	}
	for _, req := range want {
		if _, err := w.Write(req); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readAll(t, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("read %d requests, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Err || !reflect.DeepEqual(got[i].Req, want[i]) {
			t.Errorf("request %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestReadQueryRequestsStopsOnCallbackError(t *testing.T) {
	stop := errors.New("stop")
	calls := 0
	err := ReadQueryRequests(strings.NewReader(`{"query": "SELECT 1;"}`+"\n"+`{"query": "SELECT 2;"}`+"\n"), func(int, QueryRequest, error) error {
		calls++
		return stop
	})
	if !errors.Is(err, stop) || calls != 1 {
		t.Errorf("err = %v after %d calls, want %v after 1", err, calls, stop)
	}
}

func TestReadQueryRequestsCorruptGzip(t *testing.T) {
	data := gzipBytes(t, inputLines)
	data = data[:len(data)-10] // truncated
	if _, err := readAll(t, data); err == nil {
		t.Error("expected an error for a truncated gzip file")
	}
}

func TestRenderAndGlobInputFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"b.jsonl.gz", "a.jsonl.gz", "lab-linux-1.jsonl.gz", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{InputFiles: []string{
		filepath.Join(dir, "{{ hostname }}.jsonl.gz"),
		filepath.Join(dir, "*.jsonl.gz"),
		filepath.Join(dir, "missing-*.jsonl.gz"),
		filepath.Join(dir, "*.{{ 'jsonl' }}.gz"), // renders to a duplicate pattern
	}}

	got, err := RenderInputFiles(cfg, testVars)
	if err != nil {
		t.Fatal(err)
	}
	wantPatterns := []string{
		filepath.Join(dir, "lab-linux-1.jsonl.gz"),
		filepath.Join(dir, "*.jsonl.gz"),
		filepath.Join(dir, "missing-*.jsonl.gz"),
	}
	if !reflect.DeepEqual(got.InputFiles, wantPatterns) {
		t.Fatalf("InputFiles = %q, want %q", got.InputFiles, wantPatterns)
	}

	paths, unmatched, err := GlobInputFiles(got.InputFiles)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(dir, "lab-linux-1.jsonl.gz"),
		filepath.Join(dir, "a.jsonl.gz"),
		filepath.Join(dir, "b.jsonl.gz"),
	}
	if !reflect.DeepEqual(paths, want) {
		t.Errorf("paths = %q, want %q", paths, want)
	}
	if want := []string{filepath.Join(dir, "missing-*.jsonl.gz")}; !reflect.DeepEqual(unmatched, want) {
		t.Errorf("unmatched = %q, want %q", unmatched, want)
	}
}

func TestRenderInputFilesErrors(t *testing.T) {
	for name, tc := range map[string]struct{ file, wantErr string }{
		"unknown variable": {"{{ nope.x }}.jsonl.gz", `render "{{ nope.x }}.jsonl.gz"`},
		"empty path":       {"{{ '' }}", "rendered to an empty path"},
		"bad pattern":      {"queries/[.jsonl.gz", "invalid pattern"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := RenderInputFiles(Config{InputFiles: []string{tc.file}}, testVars)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// readFrom calls readQueryRequests and collects the lines passed to fn.
func readFrom(t *testing.T, data []byte, after int, unterminated bool) (got []readLine, last int, pending bool, err error) {
	t.Helper()
	last, pending, err = readQueryRequests(bytes.NewReader(data), after, unterminated, func(line int, req QueryRequest, err error) error {
		got = append(got, readLine{Line: line, Req: req, Err: err != nil})
		return nil
	})
	return got, last, pending, err
}

func TestReadQueryRequestsResumes(t *testing.T) {
	for name, encode := range map[string]func(string) []byte{
		"plain": func(s string) []byte { return []byte(s) },
		"gzip":  func(s string) []byte { return gzipBytes(t, s) },
	} {
		t.Run(name, func(t *testing.T) {
			got, last, pending, err := readFrom(t, encode(inputLines), 3, false)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, wantInputLines[2:]) || last != 5 || pending {
				t.Errorf("got %+v, last %d, pending %v; want %+v, 5, false", got, last, pending, wantInputLines[2:])
			}

			// Nothing new.
			got, last, _, err = readFrom(t, encode(inputLines), 5, false)
			if err != nil || len(got) != 0 || last != 5 {
				t.Errorf("got %+v, last %d, err %v; want nothing, 5, nil", got, last, err)
			}

			// The input has fewer lines than were already read.
			if _, _, _, err := readFrom(t, encode(inputLines), 9, false); err == nil || !strings.Contains(err.Error(), "fewer than the 9 already read") {
				t.Errorf("err = %v, want a shrunk input error", err)
			}
		})
	}
}

func TestReadQueryRequestsUnterminatedLine(t *testing.T) {
	data := []byte(`{"query": "SELECT 1;"}` + "\n" + `{"query": "SELECT 2;"}`)

	got, last, pending, err := readFrom(t, data, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []readLine{{Line: 1, Req: QueryRequest{Query: "SELECT 1;"}}}
	if !reflect.DeepEqual(got, want) || last != 1 || !pending {
		t.Errorf("got %+v, last %d, pending %v; want %+v, 1, true", got, last, pending, want)
	}

	got, last, pending, err = readFrom(t, data, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	want = []readLine{{Line: 2, Req: QueryRequest{Query: "SELECT 2;"}}}
	if !reflect.DeepEqual(got, want) || last != 2 || pending {
		t.Errorf("got %+v, last %d, pending %v; want %+v, 2, false", got, last, pending, want)
	}
}

func TestReadQueryRequestsDropsPartialLineOnReadError(t *testing.T) {
	// A complete first member followed by a truncated one, as when a gzip file is read
	// while a member is being appended to it.
	var long strings.Builder
	for i := range 2000 {
		fmt.Fprintf(&long, "%x", i*7919)
	}
	second := gzipBytes(t, `{"query": "SELECT '`+long.String()+`';"}`+"\n")
	data := append(gzipBytes(t, `{"query": "SELECT 1;"}`+"\n"), second[:len(second)/2]...)

	got, last, pending, err := readFrom(t, data, 0, true)
	if err == nil {
		t.Error("expected an error for a truncated gzip member")
	}
	want := []readLine{{Line: 1, Req: QueryRequest{Query: "SELECT 1;"}}}
	if !reflect.DeepEqual(got, want) || last != 1 || pending {
		t.Errorf("got %+v, last %d, pending %v; want %+v, 1, false", got, last, pending, want)
	}
}

func TestIsJSONInputFile(t *testing.T) {
	for path, want := range map[string]bool{
		"queries.json":        true,
		"queries.json.gz":     true,
		"dir/QUERIES.JSON.GZ": true,
		"queries.jsonl":       false,
		"queries.jsonl.gz":    false,
		"queries.json.bak":    false,
		"queries":             false,
		"queries.gz":          false,
	} {
		if got := isJSONInputFile(path); got != want {
			t.Errorf("isJSONInputFile(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestReadJSONQueryRequests(t *testing.T) {
	read := func(data []byte) ([]readLine, error) {
		raws, err := readJSONQueryRequests(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		var got []readLine
		for i, raw := range raws {
			req, err := ParseQueryRequest(raw)
			got = append(got, readLine{Line: i + 1, Req: req, Err: err != nil})
		}
		return got, nil
	}
	want := []readLine{
		{Line: 1, Req: QueryRequest{Query: "SELECT 1;", Platforms: []string{"linux"}}},
		{Line: 2, Req: QueryRequest{Query: "SELECT 2;"}},
	}
	for name, body := range map[string]string{
		"array":        `[{"query": "SELECT 1;", "platforms": ["linux"]}, {"query": " SELECT 2; "}]`,
		"pretty array": "[\n  {\n    \"query\": \"SELECT 1;\",\n    \"platforms\": [\"linux\"]\n  },\n  {\"query\": \"SELECT 2;\"}\n]\n",
		"objects":      `{"query": "SELECT 1;", "platforms": ["linux"]}` + "\n" + `{"query": "SELECT 2;"}` + "\n",
		"mixed":        `{"query": "SELECT 1;", "platforms": ["linux"]} [{"query": "SELECT 2;"}]`,
	} {
		for _, gz := range []bool{false, true} {
			data := []byte(body)
			if gz {
				data = gzipBytes(t, body)
			}
			got, err := read(data)
			if err != nil {
				t.Fatalf("%s (gzip %v): %v", name, gz, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s (gzip %v): got %+v, want %+v", name, gz, got, want)
			}
		}
	}

	// Elements that are not request objects are returned, and rejected by ParseQueryRequest.
	got, err := read([]byte(`["SELECT 1;", 5, {"query": "SELECT 2;"}]`))
	if err != nil {
		t.Fatal(err)
	}
	wantMixed := []readLine{{Line: 1, Err: true}, {Line: 2, Err: true}, {Line: 3, Req: QueryRequest{Query: "SELECT 2;"}}}
	if !reflect.DeepEqual(got, wantMixed) {
		t.Errorf("got %+v, want %+v", got, wantMixed)
	}

	if got, err := read(nil); err != nil || len(got) != 0 {
		t.Errorf("empty file: got %+v, %v; want nothing", got, err)
	}
	for _, bad := range []string{`[{"query": "SELECT 1;"},`, `{"query": `, `SELECT 1;`} {
		if got, err := read([]byte(bad)); err == nil {
			t.Errorf("%q: got %+v, want an error", bad, got)
		}
	}
}

func TestRequestSum(t *testing.T) {
	a := requestSum(json.RawMessage(`{"query": "SELECT 1;", "platforms": ["linux"]}`))
	b := requestSum(json.RawMessage("{\n  \"query\":\"SELECT 1;\",\n  \"platforms\": [ \"linux\" ]\n}"))
	if a != b {
		t.Error("sums differ for requests that differ only in whitespace")
	}
	// Whitespace inside strings is significant.
	if c := requestSum(json.RawMessage(`{"query": "SELECT  1;", "platforms": ["linux"]}`)); a == c {
		t.Error("sums equal for different requests")
	}
}

func TestCheckInputOutputOverlap(t *testing.T) {
	outputs := []string{"data/2026-11-21/osquery-results.jsonl.gz", "results/all.jsonl.gz"}
	if err := CheckInputOutputOverlap([]string{"queries/*.jsonl", "data/*/queries.json"}, outputs); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	err := CheckInputOutputOverlap([]string{"data/*/*.jsonl.gz", "queries/*.jsonl", "./results/*"}, outputs)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{`"data/*/*.jsonl.gz" matches output file "data/2026-11-21/osquery-results.jsonl.gz"`, `"./results/*" matches output file "results/all.jsonl.gz"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to contain %s", err, want)
		}
	}
	if strings.Contains(err.Error(), "queries/") {
		t.Errorf("err = %v names a pattern that matches no output", err)
	}
}
