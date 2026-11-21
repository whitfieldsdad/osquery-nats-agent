package internal

import (
	"reflect"
	"strings"
	"testing"
)

var testVars = TemplateVars{
	Platform:   "linux",
	Hostname:   "lab-linux-1",
	HostID:     "cb59def8-f1e0-4659-90bf-f81dfce2e6c6",
	IPAddress:  "192.168.82.22",
	MACAddress: "10:7c:61:21:5c:e1",
}

func TestRenderTemplate(t *testing.T) {
	for tpl, want := range map[string]string{
		"osquery-results":                                      "osquery-results",
		"osquery-results.{{ platform }}":                       "osquery-results.linux",
		"osquery-results.{{hostname}}":                         "osquery-results.lab-linux-1",
		"osquery-results.{{ host_id }}":                        "osquery-results.cb59def8-f1e0-4659-90bf-f81dfce2e6c6",
		"osquery-results.{{ ip_address }}":                     "osquery-results.192.168.82.22",
		"osquery-results.{{ ip_address | replace('.', '-') }}": "osquery-results.192-168-82-22",
		"osquery-results.{{ mac_address | replace(':', '') }}": "osquery-results.107c61215ce1",
		"osquery-results.{{ hostname | upper }}":               "osquery-results.LAB-LINUX-1",
		"osquery.{{ platform }}.{{ hostname }}.results":        "osquery.linux.lab-linux-1.results",
	} {
		got, err := RenderTemplate(tpl, testVars)
		if err != nil {
			t.Errorf("RenderTemplate(%q): %v", tpl, err)
		} else if got != want {
			t.Errorf("RenderTemplate(%q) = %q, want %q", tpl, got, want)
		}
	}
}

func TestRenderSubjectErrors(t *testing.T) {
	for tpl, wantErr := range map[string]string{
		"osquery-results.{{ hostnme }}": `Unable to evaluate name "hostnme" (available variables: host_id, hostname, ip_address, mac_address, platform)`,
		"osquery-results.{{ hostname ":  "",
	} {
		_, err := RenderTemplate(tpl, testVars)
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("RenderTemplate(%q) err = %v, want it to contain %q", tpl, err, wantErr)
		}
	}

	// Unknown values are undefined, not empty.
	if _, err := RenderTemplate("r.{{ ip_address }}", TemplateVars{Platform: "linux"}); err == nil {
		t.Error("rendering an unknown ip_address succeeded, want an error")
	}
}

func TestRenderSubjectCannotReadFiles(t *testing.T) {
	if out, err := RenderTemplate(`{% include "/etc/hostname" %}`, testVars); err == nil {
		t.Errorf("include rendered %q, want an error", out)
	}
}

func TestResolveSubjects(t *testing.T) {
	cfg := Config{
		InputSubjects:  []string{"osquery-queries", "osquery-queries.{{ hostname }}", "osquery-queries.{{ platform }}.>"},
		OutputSubjects: []string{"osquery-results.{{ platform }}", "osquery-results.{{ ip_address | replace('.', '-') }}", "osquery-results.linux"},
	}
	got, err := ResolveSubjects(cfg, testVars)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"osquery-queries", "osquery-queries.lab-linux-1", "osquery-queries.linux.>"}; !reflect.DeepEqual(got.InputSubjects, want) {
		t.Errorf("InputSubjects = %q, want %q", got.InputSubjects, want)
	}
	// "osquery-results.linux" is listed twice once rendered; it is published to once.
	if want := []string{"osquery-results.linux", "osquery-results.192-168-82-22"}; !reflect.DeepEqual(got.OutputSubjects, want) {
		t.Errorf("OutputSubjects = %q, want %q", got.OutputSubjects, want)
	}
}

func TestResolveSubjectsErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		in, out string
		wantErr string
	}{
		"same subject":            {"q", "q", "overlaps"},
		"wildcard loop":           {"osquery.>", "osquery.results", "overlaps"},
		"rendered overlap":        {"osquery.{{ platform }}", "osquery.linux", "overlaps"},
		"publish to wildcard":     {"q", "r.*", "cannot publish to a wildcard subject"},
		"undefined variable":      {"q", "r.{{ hostnme }}", `Unable to evaluate name "hostnme"`},
		"empty token":             {"q", "r.{{ platform | replace('linux', '') }}", "empty token"},
		"whitespace":              {"q", "r.{{ 'a b' }}", "whitespace"},
		"date in subject":         {"q", "r.{{ date }}", `Unable to evaluate name "date"`},
		"time in subject":         {"q", "r.{{ time }}", `Unable to evaluate name "time"`},
		"clock_time in subject":   {"q", "r.{{ clock_time }}", `Unable to evaluate name "clock_time"`},
		"misplaced full wildcard": {"q.>.x", "r", `">" must be the last token`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ResolveSubjects(Config{InputSubjects: []string{tc.in}, OutputSubjects: []string{tc.out}}, testVars)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}
