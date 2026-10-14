// Package templates renders Dockerfile templates and ships the built-in ones.
//
// A template body is a Go text/template. Every declared variable is available
// as a top-level field, e.g. {{ .NODE_VERSION }}, and conditionals work as
// usual: {{ if .BUILD_COMMAND }}RUN {{ .BUILD_COMMAND }}{{ end }}.
package templates

import (
	"bytes"
	"fmt"
	"regexp"
	"text/template"

	"shipyard/internal/store"
)

var varName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Validate checks the variable declarations and that the body parses.
func Validate(t *store.Template) error {
	if t.Name == "" {
		return fmt.Errorf("name is required")
	}
	if t.Dockerfile == "" {
		return fmt.Errorf("dockerfile is required")
	}
	seen := map[string]bool{}
	for _, v := range t.Variables {
		if !varName.MatchString(v.Name) {
			return fmt.Errorf("invalid variable name %q (letters, digits and _ only)", v.Name)
		}
		if seen[v.Name] {
			return fmt.Errorf("variable %q is declared twice", v.Name)
		}
		seen[v.Name] = true
	}
	if _, err := parse(t.Dockerfile); err != nil {
		return err
	}
	return nil
}

// Values merges the template defaults with the project overrides. Overrides for
// variables the template no longer declares are ignored.
func Values(t *store.Template, overrides map[string]string) map[string]string {
	vals := make(map[string]string, len(t.Variables))
	for _, v := range t.Variables {
		vals[v.Name] = v.Default
		if o, ok := overrides[v.Name]; ok {
			vals[v.Name] = o
		}
	}
	return vals
}

// Render produces the final Dockerfile for the given overrides.
func Render(t *store.Template, overrides map[string]string) (string, error) {
	tpl, err := parse(t.Dockerfile)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, Values(t, overrides)); err != nil {
		return "", fmt.Errorf("render template: %w", err)
	}
	return buf.String(), nil
}

func parse(body string) (*template.Template, error) {
	tpl, err := template.New("Dockerfile").Option("missingkey=error").Parse(body)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}
	return tpl, nil
}
