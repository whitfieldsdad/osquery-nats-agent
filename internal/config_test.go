package internal

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfig(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{
		"input_subjects": ["q.a", " q.b ", "q.a"],
		"output_subjects": ["r"],
		"query_timeout": "5s"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"q.a", "q.b"}; !reflect.DeepEqual(cfg.InputSubjects, want) {
		t.Errorf("InputSubjects = %q, want %q", cfg.InputSubjects, want)
	}
	if cfg.NATSURL != DefaultNATSURL || !reflect.DeepEqual(cfg.OutputFiles, []string{DefaultOutputFile}) {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	if time.Duration(cfg.QueryTimeout) != 5*time.Second {
		t.Errorf("QueryTimeout = %v, want 5s", time.Duration(cfg.QueryTimeout))
	}
}

func TestLoadConfigInputFilePollInterval(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{"input_subjects": ["q"], "output_subjects": ["r"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.InputFilePollInterval != DefaultInputFilePollInterval {
		t.Errorf("InputFilePollInterval = %v, want the default %v",
			time.Duration(cfg.InputFilePollInterval), time.Duration(DefaultInputFilePollInterval))
	}

	cfg, err = LoadConfig(writeConfig(t, `{
		"input_subjects": ["q"],
		"output_subjects": ["r"],
		"input_file_poll_interval": "1m"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if time.Duration(cfg.InputFilePollInterval) != time.Minute {
		t.Errorf("InputFilePollInterval = %v, want 1m0s", time.Duration(cfg.InputFilePollInterval))
	}
}

func TestLoadConfigOutputFiles(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{
		"input_subjects": ["q"],
		"output_subjects": ["r"],
		"output_files": [" a/{{ date }}.jsonl.gz ", "b/{{ hostname }}.jsonl.gz", "a/{{ date }}.jsonl.gz"]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a/{{ date }}.jsonl.gz", "b/{{ hostname }}.jsonl.gz"}; !reflect.DeepEqual(cfg.OutputFiles, want) {
		t.Errorf("OutputFiles = %q, want %q", cfg.OutputFiles, want)
	}

	// An empty list turns off output files rather than getting the default.
	cfg, err = LoadConfig(writeConfig(t, `{"input_subjects": ["q"], "output_subjects": ["r"], "output_files": []}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OutputFiles == nil || len(cfg.OutputFiles) != 0 {
		t.Errorf("OutputFiles = %#v, want an empty list", cfg.OutputFiles)
	}
}

func TestLoadConfigInputFiles(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{
		"input_subjects": ["q"],
		"output_subjects": ["r"],
		"input_files": [" queries/*.jsonl.gz ", "queries/{{ hostname }}.jsonl.gz", "queries/*.jsonl.gz"]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"queries/*.jsonl.gz", "queries/{{ hostname }}.jsonl.gz"}; !reflect.DeepEqual(cfg.InputFiles, want) {
		t.Errorf("InputFiles = %q, want %q", cfg.InputFiles, want)
	}
}

func TestLoadConfigErrors(t *testing.T) {
	for name, tc := range map[string]struct{ body, wantErr string }{
		"old keys":                {`{"input": "q", "output": "r"}`, `unknown field "input"`},
		"no inputs":               {`{"output_subjects": ["r"]}`, `no queries to run`},
		"empty inputs":            {`{"input_subjects": [], "input_files": [], "output_subjects": ["r"]}`, `no queries to run`},
		"no outputs":              {`{"input_subjects": ["q"], "output_files": []}`, `nowhere to send results`},
		"empty subject":           {`{"input_subjects": [""], "output_subjects": ["r"]}`, `contains an empty subject`},
		"bad template":            {`{"input_subjects": ["q.{{ hostname"], "output_subjects": ["r"]}`, `subject template "q.{{ hostname"`},
		"bad duration":            {`{"input_subjects": ["q"], "output_subjects": ["r"], "query_timeout": 60}`, `duration`},
		"tls cert no key":         {`{"input_subjects": ["q"], "output_subjects": ["r"], "nats_tls_cert_path": "c.pem"}`, `must be set together`},
		"tls key no cert":         {`{"input_subjects": ["q"], "output_subjects": ["r"], "nats_tls_key_path": "k.pem"}`, `must be set together`},
		"empty input file":        {`{"input_subjects": ["q"], "output_subjects": ["r"], "input_files": [" "]}`, `"input_files" contains an empty path`},
		"bad input file template": {`{"input_subjects": ["q"], "output_subjects": ["r"], "input_files": ["q/{{ hostname"]}`, `input_files template "q/{{ hostname"`},
		"old output_file key":     {`{"input_subjects": ["q"], "output_subjects": ["r"], "output_file": "r.jsonl.gz"}`, `unknown field "output_file"`},
		"negative cache size":     {`{"input_subjects": ["q"], "max_cached_queries": -1}`, `"max_cached_queries" must be a positive number`},
		"empty output file":       {`{"input_subjects": ["q"], "output_subjects": ["r"], "output_files": ["a.jsonl.gz", " "]}`, `"output_files" contains an empty path`},
		"bad output template":     {`{"input_subjects": ["q"], "output_subjects": ["r"], "output_files": ["{{ date"]}`, `output_files template "{{ date"`},
		"negative poll interval":  {`{"input_subjects": ["q"], "output_subjects": ["r"], "input_file_poll_interval": "-1s"}`, `"input_file_poll_interval" must not be negative`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LoadConfig(writeConfig(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestSubjectsOverlap(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"a.b", "a.b", true},
		{"a.b", "a.c", false},
		{"a.*", "a.b", true},
		{"a.*", "a.b.c", false},
		{"a.>", "a.b.c", true},
		{"a.>", "a", false},
		{">", "anything.at.all", true},
		{"*.results", "osquery.queries", false},
		{"a", "a.b", false},
	} {
		if got := subjectsOverlap(tc.a, tc.b); got != tc.want {
			t.Errorf("subjectsOverlap(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
		if got := subjectsOverlap(tc.b, tc.a); got != tc.want {
			t.Errorf("subjectsOverlap(%q, %q) = %v, want %v", tc.b, tc.a, got, tc.want)
		}
	}
}

func TestLoadConfigScheduledQueries(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{
		"input_subjects": ["q"],
		"output_subjects": ["r"],
		"scheduled_queries": [
			{"name": " os ", "query": " SELECT name, version FROM os_version; ", "interval": 60, "platforms": ["linux"]},
			{"name": "uptime", "query": "SELECT * FROM uptime;", "interval": 5},
			{"query": "SELECT * FROM time;", "interval": 10},
			{"name": " ", "query": "SELECT * FROM time;", "interval": 10}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []ScheduledQuery{
		{Name: "os", Query: "SELECT name, version FROM os_version;", Interval: 60, Platforms: []string{"linux"}},
		{Name: "uptime", Query: "SELECT * FROM uptime;", Interval: 5},
		{Query: "SELECT * FROM time;", Interval: 10},
		{Query: "SELECT * FROM time;", Interval: 10},
	}
	if !reflect.DeepEqual(cfg.ScheduledQueries, want) {
		t.Errorf("ScheduledQueries = %+v, want %+v", cfg.ScheduledQueries, want)
	}
}

func TestLoadConfigScheduledQueryErrors(t *testing.T) {
	for name, tc := range map[string]struct{ queries, wantErr string }{
		"missing query, no name": {`[{"interval": 5}]`, `scheduled_queries[0]: "query" is required`},
		"duplicate name":         {`[{"name": "a", "query": "SELECT 1;", "interval": 5}, {"name": "a", "query": "SELECT 2;", "interval": 5}]`, `"a": name is used more than once`},
		"missing query":          {`[{"name": "a", "interval": 5}]`, `"query" is required`},
		"missing interval":       {`[{"name": "a", "query": "SELECT 1;"}]`, `"interval" must be a positive number of seconds`},
		"string interval":        {`[{"name": "a", "query": "SELECT 1;", "interval": "60s"}]`, `cannot unmarshal string`},
		"unknown field":          {`[{"name": "a", "query": "SELECT 1;", "interval": 5, "splay": 1}]`, `unknown field "splay"`},
	} {
		t.Run(name, func(t *testing.T) {
			body := `{"input_subjects": ["q"], "output_subjects": ["r"], "scheduled_queries": ` + tc.queries + `}`
			_, err := LoadConfig(writeConfig(t, body))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadConfigInputsAndOutputs(t *testing.T) {
	for name, body := range map[string]string{
		"nats in, nats out":       `{"input_subjects": ["q"], "output_subjects": ["r"], "output_files": []}`,
		"nats in, files out":      `{"input_subjects": ["q"], "output_files": ["r.jsonl.gz"]}`,
		"nats in, default file":   `{"input_subjects": ["q"]}`,
		"files in, nats out":      `{"input_files": ["q.jsonl"], "output_subjects": ["r"], "output_files": []}`,
		"files in, files out":     `{"input_files": ["q.jsonl"], "output_files": ["r.jsonl.gz"]}`,
		"scheduled in, files out": `{"scheduled_queries": [{"query": "SELECT 1;", "interval": 60}]}`,
		"everything":              `{"input_subjects": ["q"], "input_files": ["q.jsonl"], "output_subjects": ["r"], "output_files": ["r.jsonl.gz"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadConfig(writeConfig(t, body)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLoadConfigYAML(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	const yamlBody = `
nats_url: nats://nats.example:4222
input_subjects: [osquery.queries]
output_subjects:
  - osquery.results.{{ platform }}
output_files: []
query_timeout: 30s
scheduled_queries:
  - name: processes
    query: |
      SELECT pid, name
      FROM processes;
    interval: 15
    platforms: [linux]
`
	const jsonBody = `{
		"nats_url": "nats://nats.example:4222",
		"input_subjects": ["osquery.queries"],
		"output_subjects": ["osquery.results.{{ platform }}"],
		"output_files": [],
		"query_timeout": "30s",
		"scheduled_queries": [
			{"name": "processes", "query": "SELECT pid, name\nFROM processes;\n", "interval": 15, "platforms": ["linux"]}
		]
	}`
	want, err := LoadConfig(write("config.json", jsonBody))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.yaml", "config.yml", "CONFIG.YAML"} {
		got, err := LoadConfig(write(name, yamlBody))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %+v, want %+v", name, got, want)
		}
	}

	for name, tc := range map[string]struct{ body, wantErr string }{
		"unknown key":    {"input_subjects: [q]\noutput_subject: [r]\n", `unknown field "output_subject"`},
		"bad duration":   {"input_subjects: [q]\nquery_timeout: 60\n", "duration"},
		"bad syntax":     {"input_subjects: [q\n", "yaml"},
		"two documents":  {"input_subjects: [q]\n---\noutput_files: []\n", "more than one YAML document"},
		"empty":          {"", "no queries to run"},
		"not a mapping":  {"- q\n", "cannot unmarshal array"},
		"validation too": {"input_subjects: [q]\noutput_files: []\n", "nowhere to send results"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LoadConfig(write("bad.yaml", tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadConfigExamples(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "examples", "config", "*", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Skip("no example configs")
	}
	for _, path := range paths {
		if _, err := LoadConfig(path); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
}

func TestLoadConfigMaxCachedQueries(t *testing.T) {
	cfg, err := LoadConfig(writeConfig(t, `{"input_subjects": ["q"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxCachedQueries != DefaultMaxCachedQueries || DefaultMaxCachedQueries != 10000 {
		t.Errorf("MaxCachedQueries = %d, want the default of 10000", cfg.MaxCachedQueries)
	}
	if cfg, err = LoadConfig(writeConfig(t, `{"input_subjects": ["q"], "max_cached_queries": 50}`)); err != nil {
		t.Fatal(err)
	}
	if cfg.MaxCachedQueries != 50 {
		t.Errorf("MaxCachedQueries = %d, want 50", cfg.MaxCachedQueries)
	}
}

func TestLoadConfigWarnsAboutLegacyDefaultOutputFile(t *testing.T) {
	t.Chdir(t.TempDir())
	body := `{"input_subjects": ["q"]}`
	cfg, err := LoadConfig(writeConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Warnings) != 0 {
		t.Errorf("Warnings = %q with no old results file, want none", cfg.Warnings)
	}

	if err := os.WriteFile(legacyDefaultOutputFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if cfg, err = LoadConfig(writeConfig(t, body)); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "results are now written to") {
		t.Errorf("Warnings = %q, want one about the new default output file", cfg.Warnings)
	}

	// Setting output_files explicitly silences it.
	if cfg, err = LoadConfig(writeConfig(t, `{"input_subjects": ["q"], "output_files": ["r.jsonl.gz"]}`)); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Warnings) != 0 {
		t.Errorf("Warnings = %q with output_files set, want none", cfg.Warnings)
	}
}
