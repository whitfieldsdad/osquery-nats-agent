package internal

import (
	"errors"
	"fmt"
	"strings"
)

// ResolveSubjects renders cfg's input and output subject templates with vars and validates
// the results: every subject must be a valid NATS subject, output subjects cannot contain
// wildcards, and no output subject may overlap an input subject.
func ResolveSubjects(cfg Config, vars TemplateVars) (Config, error) {
	var errs []error
	render := func(field string, templates []string, publish bool) []string {
		seen := make(map[string]bool, len(templates))
		var out []string
		for _, tpl := range templates {
			subject, err := RenderTemplate(tpl, vars)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: render %q: %w", field, tpl, err))
				continue
			}
			if err := validateSubject(subject, publish); err != nil {
				errs = append(errs, fmt.Errorf("%s: %q rendered to invalid subject %q: %w", field, tpl, subject, err))
				continue
			}
			if !seen[subject] {
				seen[subject] = true
				out = append(out, subject)
			}
		}
		return out
	}
	cfg.InputSubjects = render("input_subjects", cfg.InputSubjects, false)
	cfg.OutputSubjects = render("output_subjects", cfg.OutputSubjects, true)

	for _, out := range cfg.OutputSubjects {
		for _, in := range cfg.InputSubjects {
			if subjectsOverlap(in, out) {
				errs = append(errs, fmt.Errorf("output subject %q overlaps input subject %q", out, in))
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// validateSubject checks NATS subject syntax: dot-separated, non-empty tokens without
// whitespace. Wildcards ("*" and a trailing ">") are only allowed when subscribing.
func validateSubject(subject string, publish bool) error {
	if subject == "" {
		return errors.New("subject is empty")
	}
	if strings.ContainsAny(subject, " \t\r\n") {
		return errors.New("subject contains whitespace")
	}
	tokens := strings.Split(subject, ".")
	for i, tok := range tokens {
		switch {
		case tok == "":
			return errors.New("subject has an empty token")
		case tok == "*" || tok == ">":
			if publish {
				return errors.New("cannot publish to a wildcard subject")
			}
			if tok == ">" && i != len(tokens)-1 {
				return errors.New(`">" must be the last token`)
			}
		case strings.ContainsAny(tok, "*>") && publish:
			return errors.New(`subject contains "*" or ">"`)
		}
	}
	return nil
}
