package internal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	DefaultNATSURL       = "nats://127.0.0.1:4222"
	DefaultInputSubject  = "osquery-queries"
	DefaultOutputSubject = "osquery-results"
	DefaultOutputFile    = "osquery-results.jsonl.gz"
	DefaultQueryTimeout  = Duration(time.Minute)
)

// DefaultOsqueryiPath is where osqueryi is found by default: the standard install location on
// Windows (where it is not on PATH), and "osqueryi" on PATH elsewhere.
func DefaultOsqueryiPath() string {
	if runtime.GOOS == "windows" {
		return `C:\Program Files\osquery\osqueryi.exe`
	}
	return "osqueryi"
}

// DefaultConfig returns the configuration written when no config file exists.
func DefaultConfig() Config {
	return Config{
		NATSURL:        DefaultNATSURL,
		InputSubjects:  []string{DefaultInputSubject},
		OutputSubjects: []string{DefaultOutputSubject},
		OutputFile:     DefaultOutputFile,
		OsqueryiPath:   DefaultOsqueryiPath(),
		QueryTimeout:   DefaultQueryTimeout,
	}
}

// Config is the agent configuration, loaded from a JSON file.
type Config struct {
	// NATSURL is the NATS server URL (comma-separate multiple servers).
	NATSURL string `json:"nats_url"`
	// NATSCredentialsPath is an optional path to a NATS .creds file.
	NATSCredentialsPath string `json:"nats_credentials_path,omitempty"`
	// InputSubjects are the subjects the agent reads queries from (wildcards allowed).
	InputSubjects []string `json:"input_subjects"`
	// OutputSubjects are the subjects every result is published to.
	OutputSubjects []string `json:"output_subjects"`
	// OutputFile is the local gzip-compressed JSONL file results are appended to.
	OutputFile string `json:"output_file"`
	// OsqueryiPath is the osqueryi executable (defaults to "osqueryi" on PATH).
	OsqueryiPath string `json:"osqueryi_path"`
	// QueryTimeout bounds how long a single osqueryi run may take, e.g. "30s".
	QueryTimeout Duration `json:"query_timeout"`
}

// LoadOrCreateConfig loads the config file at path. If it does not exist, it first writes
// DefaultConfig to path (creating parent directories) and reports created = true.
func LoadOrCreateConfig(path string) (cfg Config, created bool, err error) {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		switch err := WriteConfig(path, DefaultConfig()); {
		case err == nil:
			created = true
		case errors.Is(err, fs.ErrExist):
			// Created by someone else since the Stat; load theirs.
		default:
			return Config{}, false, err
		}
	}
	cfg, err = LoadConfig(path)
	return cfg, created, err
}

// WriteConfig writes cfg to path as indented JSON. It never overwrites an existing file: if
// path already exists it returns an error wrapping fs.ErrExist.
func WriteConfig(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "    ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create config directory: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create config: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		os.Remove(path)
		return fmt.Errorf("write config: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// LoadConfig reads the config file at path, applies defaults and validates it.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields() // catch typos and the old "input"/"output" keys
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}

	if cfg.NATSURL == "" {
		cfg.NATSURL = DefaultNATSURL
	}
	if cfg.OutputFile == "" {
		cfg.OutputFile = DefaultOutputFile
	}
	if cfg.OsqueryiPath == "" {
		cfg.OsqueryiPath = DefaultOsqueryiPath()
	}
	if cfg.QueryTimeout <= 0 {
		cfg.QueryTimeout = DefaultQueryTimeout
	}

	var errs []error
	cfg.InputSubjects, errs = cleanSubjects("input_subjects", cfg.InputSubjects, errs)
	cfg.OutputSubjects, errs = cleanSubjects("output_subjects", cfg.OutputSubjects, errs)
	for _, out := range cfg.OutputSubjects {
		for _, in := range cfg.InputSubjects {
			if subjectsOverlap(in, out) {
				errs = append(errs, fmt.Errorf("output subject %q overlaps input subject %q", out, in))
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return cfg, nil
}

// cleanSubjects trims and de-duplicates subjects, recording an error if none are left or
// one is blank.
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
	if len(subjects) == 0 {
		errs = append(errs, fmt.Errorf("%q must list at least one subject", field))
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
