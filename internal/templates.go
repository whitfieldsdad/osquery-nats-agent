package internal

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nikolalohinski/gonja/v2"
	"github.com/nikolalohinski/gonja/v2/config"
	"github.com/nikolalohinski/gonja/v2/exec"
	"github.com/nikolalohinski/gonja/v2/loaders"
)

// Subjects and the output file path are Jinja templates, rendered with gonja.

// templateConfig is Jinja's default syntax with StrictUndefined set, so that a misspelled
// variable is an error instead of rendering as an empty string.
var templateConfig = func() *config.Config {
	c := config.New()
	c.StrictUndefined = true
	return c
}()

// Formats of the {{ date }}, {{ time }} and {{ clock_time }} template variables.
const (
	DateFormat      = "2006-01-02"
	TimeFormat      = "2006-01-02T15:04:05"
	ClockTimeFormat = "15:04:05"
)

// TemplateVars are the endpoint data properties available to templates.
type TemplateVars struct {
	Platform   string
	Hostname   string
	HostID     string
	IPAddress  string
	MACAddress string
	// Date, Time and ClockTime are the current UTC date (YYYY-MM-DD), timestamp
	// (YYYY-MM-DDTHH:MM:SS) and time of day (HH:MM:SS). They are only set when rendering the
	// output file path: subjects are rendered once at startup, so they would go stale.
	Date      string
	Time      string
	ClockTime string
}

// CurrentTemplateVars collects this host's template variables. Date, Time and ClockTime are
// left unset. If the
// primary IP/MAC address cannot be determined, the other variables are still returned
// along with the error.
func CurrentTemplateVars(hostID string) (TemplateVars, error) {
	host := CurrentHost(hostID)
	vars := TemplateVars{Platform: host.Platform, Hostname: host.Hostname, HostID: host.ID}
	ip, mac, err := PrimaryAddress()
	if err != nil {
		return vars, err
	}
	vars.IPAddress, vars.MACAddress = ip, mac
	return vars, nil
}

// values returns the template variables by name. Unknown values are left out rather than
// set to "", so a template that uses one fails with an error instead of rendering "".
func (v TemplateVars) values() map[string]any {
	vars := map[string]any{}
	for name, value := range map[string]string{
		"platform":    v.Platform,
		"hostname":    v.Hostname,
		"host_id":     v.HostID,
		"ip_address":  v.IPAddress,
		"mac_address": v.MACAddress,
		"date":        v.Date,
		"time":        v.Time,
		"clock_time":  v.ClockTime,
	} {
		if value != "" {
			vars[name] = value
		}
	}
	return vars
}

// parseTemplate compiles a Jinja template. Templates are loaded from memory only, so
// {% include %} and friends cannot read local files.
func parseTemplate(source string) (*exec.Template, error) {
	loader, err := loaders.NewMemoryLoader(map[string]string{"/template": source})
	if err != nil {
		return nil, err
	}
	return exec.NewTemplate("/template", templateConfig, loader, gonja.DefaultEnvironment)
}

func executeTemplate(tpl *exec.Template, vars TemplateVars) (string, error) {
	values := vars.values()
	out, err := tpl.ExecuteToString(exec.NewContext(values))
	if err != nil {
		names := make([]string, 0, len(values))
		for name := range values {
			names = append(names, name)
		}
		slices.Sort(names)
		return "", fmt.Errorf("%w (available variables: %s)", err, strings.Join(names, ", "))
	}
	return strings.TrimSpace(out), nil
}

// RenderTemplate renders a Jinja template with vars.
func RenderTemplate(source string, vars TemplateVars) (string, error) {
	tpl, err := parseTemplate(source)
	if err != nil {
		return "", err
	}
	return executeTemplate(tpl, vars)
}

// OutputPathTemplate renders the output file path for a given time, with {{ date }},
// {{ time }} and {{ clock_time }} set from that time in UTC. The path is re-rendered at most once per second.
type OutputPathTemplate struct {
	tpl  *exec.Template
	vars TemplateVars

	mu         sync.Mutex
	lastSecond time.Time
	lastPath   string
}

func NewOutputPathTemplate(source string, vars TemplateVars) (*OutputPathTemplate, error) {
	tpl, err := parseTemplate(source)
	if err != nil {
		return nil, fmt.Errorf("output_files: %q: %w", source, err)
	}
	return &OutputPathTemplate{tpl: tpl, vars: vars}, nil
}

// PathAt returns the output file path for time t.
func (o *OutputPathTemplate) PathAt(t time.Time) (string, error) {
	second := t.UTC().Truncate(time.Second)
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.lastPath != "" && second.Equal(o.lastSecond) {
		return o.lastPath, nil
	}
	vars := o.vars
	vars.Date = second.Format(DateFormat)
	vars.Time = second.Format(TimeFormat)
	vars.ClockTime = second.Format(ClockTimeFormat)
	path, err := executeTemplate(o.tpl, vars)
	if err != nil {
		return "", fmt.Errorf("output_files: %w", err)
	}
	if path == "" {
		return "", fmt.Errorf("output_files: rendered to an empty path")
	}
	o.lastSecond, o.lastPath = second, path
	return path, nil
}
