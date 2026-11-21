package internal

import (
	"errors"
	"io/fs"
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
	if cfg.NATSURL != DefaultNATSURL || cfg.OutputFile != DefaultOutputFile {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	if time.Duration(cfg.QueryTimeout) != 5*time.Second {
		t.Errorf("QueryTimeout = %v, want 5s", time.Duration(cfg.QueryTimeout))
	}
}

func TestLoadConfigErrors(t *testing.T) {
	for name, tc := range map[string]struct{ body, wantErr string }{
		"old keys":        {`{"input": "q", "output": "r"}`, `unknown field "input"`},
		"missing inputs":  {`{"output_subjects": ["r"]}`, `"input_subjects" must list at least one subject`},
		"missing outputs": {`{"input_subjects": ["q"]}`, `"output_subjects" must list at least one subject`},
		"empty subject":   {`{"input_subjects": [""], "output_subjects": ["r"]}`, `contains an empty subject`},
		"same subject":    {`{"input_subjects": ["q"], "output_subjects": ["q"]}`, `overlaps`},
		"wildcard loop":   {`{"input_subjects": ["osquery.>"], "output_subjects": ["osquery.results"]}`, `overlaps`},
		"bad duration":    {`{"input_subjects": ["q"], "output_subjects": ["r"], "query_timeout": 60}`, `duration`},
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

func TestLoadOrCreateConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "config.json")

	cfg, created, err := LoadOrCreateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("created = false for a missing config file")
	}
	if !reflect.DeepEqual(cfg, DefaultConfig()) {
		t.Errorf("generated config = %+v, want %+v", cfg, DefaultConfig())
	}

	// An existing file is loaded as-is, never overwritten.
	custom := `{"input_subjects": ["mine"], "output_subjects": ["theirs"]}`
	if err := os.WriteFile(path, []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, created, err = LoadOrCreateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if created || cfg.InputSubjects[0] != "mine" {
		t.Errorf("existing config not used: created=%v cfg=%+v", created, cfg)
	}
	if data, _ := os.ReadFile(path); string(data) != custom {
		t.Errorf("existing config was modified: %s", data)
	}
}

func TestWriteConfigDoesNotOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfig(path, DefaultConfig()); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("err = %v, want fs.ErrExist", err)
	}
}
