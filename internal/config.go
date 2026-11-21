package internal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultNATSURL      = "nats://127.0.0.1:4222"
	DefaultOutputFile   = "data/{{ date }}/osquery-results.jsonl.gz"
	DefaultQueryTimeout = Duration(time.Minute)

	DefaultMaxCachedQueries = 10000

	DefaultInputFilePollInterval = Duration(15 * time.Second)
)

// DefaultOsqueryiPath is where osqueryi is found by default: the standard install location on
// Windows (where it is not on PATH), and "osqueryi" on PATH elsewhere.
func DefaultOsqueryiPath() string {
	if runtime.GOOS == "windows" {
		return `C:\Program Files\osquery\osqueryi.exe`
	}
	return "osqueryi"
}

// Config is the agent configuration, loaded from a JSON or YAML file.
type Config struct {
	// NATSURL is the NATS server URL (comma-separate multiple servers).
	NATSURL string `json:"nats_url"`
	// NATSCredentialsPath is an optional path to a NATS .creds file.
	NATSCredentialsPath string `json:"nats_credentials_path,omitempty"`
	// NATSTLSCertPath and NATSTLSKeyPath are an optional PEM client certificate and private
	// key, presented to the NATS server for mutual TLS. Set both or neither.
	NATSTLSCertPath string `json:"nats_tls_cert_path,omitempty"`
	NATSTLSKeyPath  string `json:"nats_tls_key_path,omitempty"`
	// NATSTLSCAPath is an optional PEM file of CA certificates used to verify the NATS
	// server's certificate instead of the system's trusted roots.
	NATSTLSCAPath string `json:"nats_tls_ca_path,omitempty"`
	// InputSubjects are the subjects the agent reads queries from (wildcards allowed). Queries
	// can come from input subjects, input files, scheduled queries or any mix of them, but
	// there must be at least one source.
	InputSubjects []string `json:"input_subjects"`
	// InputFiles are JSONL files of query requests (gzip-compressed or plain), one request per
	// line in the same format as messages on the input subjects. Each entry is a glob pattern
	// and a template that can use the host metadata variables. The patterns are polled every
	// InputFilePollInterval: every query in a newly matched file is run, as is every query
	// appended to a file already read.
	InputFiles []string `json:"input_files,omitempty"`
	// InputFilePollInterval is how often the input files are polled for new queries, e.g. "15s".
	InputFilePollInterval Duration `json:"input_file_poll_interval"`
	// OutputSubjects are the subjects every result is published to. Results can go to output
	// subjects, output files or both, but there must be at least one destination. The agent
	// connects to NATS only if there are input or output subjects.
	OutputSubjects []string `json:"output_subjects"`
	// OutputFiles are the local gzip-compressed JSONL files every result is appended to. Each
	// is a Jinja template: {{ date }}, {{ time }} and {{ clock_time }} (all UTC) partition
	// results by time, e.g. a file per day with {{ date }} or per hour with {{ time[:13] }}.
	// If omitted, results are written to DefaultOutputFile; an empty list writes no files.
	OutputFiles []string `json:"output_files"`
	// OsqueryiPath is the osqueryi executable (defaults to "osqueryi" on PATH).
	OsqueryiPath string `json:"osqueryi_path"`
	// QueryTimeout bounds how long a single osqueryi run may take, e.g. "30s".
	QueryTimeout Duration `json:"query_timeout"`
	// ScheduledQueries are run periodically, in addition to queries received over NATS.
	ScheduledQueries []ScheduledQuery `json:"scheduled_queries,omitempty"`
	// Snapshot makes every run of every query deliver all of the rows it returned. By default
	// (false), queries are differential: each run delivers only the rows added and removed
	// since the query's previous successful run.
	Snapshot bool `json:"snapshot"`
	// MaxCachedQueries is how many queries received over NATS or read from input files have
	// their previous results kept, to compare their next run with. When there are more, the
	// least recently run query's results are dropped, and its next run reports every row.
	// (Scheduled queries always keep theirs.) Defaults to DefaultMaxCachedQueries. It has no
	// effect when Snapshot is set, as no previous results are kept.
	MaxCachedQueries int `json:"max_cached_queries"`

	// Warnings describes problems LoadConfig found that do not stop the config from loading,
	// for the caller to log.
	Warnings []string `json:"-"`
}

// legacyDefaultOutputFile is where older versions of the agent wrote results by default.
const legacyDefaultOutputFile = "osquery-results.jsonl.gz"

// ScheduledQuery is a query the agent runs on a fixed interval.
type ScheduledQuery struct {
	// Name optionally identifies the query; if set, it is included in each of its results
	// and must be unique.
	Name string `json:"name,omitempty"`
	// Query is the osquery SQL to run.
	Query string `json:"query"`
	// Interval is the number of seconds between runs.
	Interval int `json:"interval"`
	// Platforms the query should run on ("darwin", "linux", "windows", ...).
	// An empty list means every platform.
	Platforms []string `json:"platforms,omitempty"`
}

// LoadConfig reads the config file at path, applies defaults and validates it. A file named
// *.yaml or *.yml is read as YAML, and any other file as JSON; both use the same keys.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	if isYAMLFile(path) {
		if data, err = yamlToJSON(data); err != nil {
			return Config{}, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	var cfg Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields() // catch typos and the old "input"/"output" keys
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}

	var errs []error
	if cfg.NATSURL == "" {
		cfg.NATSURL = DefaultNATSURL
	}
	if cfg.OutputFiles == nil {
		cfg.OutputFiles = []string{DefaultOutputFile}
		// Results used to go to legacyDefaultOutputFile by default. If it is there, something
		// may still be reading it.
		if _, err := os.Stat(legacyDefaultOutputFile); err == nil {
			cfg.Warnings = append(cfg.Warnings, fmt.Sprintf(
				`results are now written to %q by default, not %q; to keep writing to %[2]q, set "output_files": [%[2]q]`,
				DefaultOutputFile, legacyDefaultOutputFile))
		}
	}
	if cfg.OsqueryiPath == "" {
		cfg.OsqueryiPath = DefaultOsqueryiPath()
	}
	if cfg.QueryTimeout <= 0 {
		cfg.QueryTimeout = DefaultQueryTimeout
	}
	if cfg.InputFilePollInterval == 0 {
		cfg.InputFilePollInterval = DefaultInputFilePollInterval
	}
	if cfg.MaxCachedQueries == 0 {
		cfg.MaxCachedQueries = DefaultMaxCachedQueries
	}
	if cfg.MaxCachedQueries < 0 {
		errs = append(errs, errors.New(`"max_cached_queries" must be a positive number`))
	}

	if cfg.InputFilePollInterval < 0 {
		errs = append(errs, errors.New(`"input_file_poll_interval" must not be negative`))
	}
	cfg.NATSTLSCertPath = strings.TrimSpace(cfg.NATSTLSCertPath)
	cfg.NATSTLSKeyPath = strings.TrimSpace(cfg.NATSTLSKeyPath)
	cfg.NATSTLSCAPath = strings.TrimSpace(cfg.NATSTLSCAPath)
	if (cfg.NATSTLSCertPath == "") != (cfg.NATSTLSKeyPath == "") {
		errs = append(errs, errors.New(`"nats_tls_cert_path" and "nats_tls_key_path" must be set together`))
	}
	cfg.InputSubjects, errs = cleanSubjects("input_subjects", cfg.InputSubjects, errs)
	cfg.OutputSubjects, errs = cleanSubjects("output_subjects", cfg.OutputSubjects, errs)
	// Subjects are Jinja templates; check their syntax now. They are rendered, and checked
	// for overlaps, by ResolveSubjects once the host's details are known.
	for _, subject := range append(append([]string{}, cfg.InputSubjects...), cfg.OutputSubjects...) {
		if _, err := parseTemplate(subject); err != nil {
			errs = append(errs, fmt.Errorf("subject template %q: %w", subject, err))
		}
	}
	cfg.InputFiles, errs = cleanPaths("input_files", cfg.InputFiles, errs)
	for _, file := range cfg.InputFiles {
		if _, err := parseTemplate(file); err != nil {
			errs = append(errs, fmt.Errorf("input_files template %q: %w", file, err))
		}
	}
	cfg.OutputFiles, errs = cleanPaths("output_files", cfg.OutputFiles, errs)
	for _, file := range cfg.OutputFiles {
		if _, err := parseTemplate(file); err != nil {
			errs = append(errs, fmt.Errorf("output_files template %q: %w", file, err))
		}
	}
	errs = validateScheduledQueries(cfg.ScheduledQueries, errs)
	if len(cfg.InputSubjects) == 0 && len(cfg.InputFiles) == 0 && len(cfg.ScheduledQueries) == 0 {
		errs = append(errs, errors.New(`no queries to run: set "input_subjects", "input_files" or "scheduled_queries"`))
	}
	if len(cfg.OutputSubjects) == 0 && len(cfg.OutputFiles) == 0 {
		errs = append(errs, errors.New(`nowhere to send results: set "output_subjects" or "output_files"`))
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return cfg, nil
}

// isYAMLFile reports whether path names a YAML file, by its extension.
func isYAMLFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return true
	}
	return false
}

// yamlToJSON converts a single YAML document to JSON, so that a YAML config is decoded (and
// validated) exactly as a JSON one is. An empty document converts to an empty object.
func yamlToJSON(data []byte) ([]byte, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc any
	if err := dec.Decode(&doc); err != nil {
		if !errors.Is(err, io.EOF) {
			return nil, err
		}
		doc = map[string]any{}
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return nil, errors.New("contains more than one YAML document")
	} else if !errors.Is(err, io.EOF) {
		return nil, err
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return json.Marshal(doc)
}

// validateScheduledQueries trims names and queries in place and checks that every scheduled
// query has a query and a positive interval, and that names, where given, are unique.
func validateScheduledQueries(queries []ScheduledQuery, errs []error) []error {
	names := make(map[string]bool, len(queries))
	for i := range queries {
		sq := &queries[i]
		sq.Name = strings.TrimSpace(sq.Name)
		sq.Query = strings.TrimSpace(sq.Query)
		label := fmt.Sprintf("scheduled_queries[%d]", i)
		if sq.Name != "" {
			label = fmt.Sprintf("scheduled query %q", sq.Name)
		}

		if sq.Name != "" {
			if names[sq.Name] {
				errs = append(errs, fmt.Errorf("%s: name is used more than once", label))
			}
			names[sq.Name] = true
		}
		if sq.Query == "" {
			errs = append(errs, fmt.Errorf("%s: \"query\" is required", label))
		}
		if sq.Interval <= 0 {
			errs = append(errs, fmt.Errorf("%s: \"interval\" must be a positive number of seconds", label))
		}
	}
	return errs
}

// cleanPaths trims and de-duplicates the input or output file templates in field,
// recording an error for a blank one. Unlike subjects, the list may be empty. A non-nil list
// stays non-nil.
func cleanPaths(field string, files []string, errs []error) ([]string, []error) {
	if files == nil {
		return nil, errs
	}
	seen := make(map[string]bool, len(files))
	out := make([]string, 0, len(files))
	for _, f := range files {
		f = strings.TrimSpace(f)
		if f == "" {
			errs = append(errs, fmt.Errorf("%q contains an empty path", field))
			continue
		}
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out, errs
}

// cleanSubjects trims and de-duplicates subjects, recording an error for a blank one. The
// list may be empty.
func cleanSubjects(field string, subjects []string, errs []error) ([]string, []error) {
	seen := make(map[string]bool, len(subjects))
	out := make([]string, 0, len(subjects))
	for _, s := range subjects {
		s = strings.TrimSpace(s)
		if s == "" {
			errs = append(errs, fmt.Errorf("%q contains an empty subject", field))
			continue
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, errs
}

// subjectsOverlap reports whether some subject could match both NATS subject patterns a and
// b (taking "*" and ">" wildcards into account). Publishing results to a subject the agent
// also reads queries from would make it consume its own output.
func subjectsOverlap(a, b string) bool {
	at, bt := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(at) && i < len(bt); i++ {
		if at[i] == ">" || bt[i] == ">" {
			return true
		}
		if at[i] != bt[i] && at[i] != "*" && bt[i] != "*" {
			return false
		}
	}
	return len(at) == len(bt)
}

// Duration is a time.Duration that is written in JSON as a string such as "30s" or "2m".
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string like \"30s\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}
