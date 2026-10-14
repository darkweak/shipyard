package templates

import (
	"strings"
	"testing"

	"shipyard/internal/store"
)

func TestRenderDefaultsAndOverrides(t *testing.T) {
	tpl := &store.Template{
		Name:       "t",
		Dockerfile: "FROM node:{{ .V }}\n{{ if .CMD }}RUN {{ .CMD }}{{ end }}",
		Variables:  []store.Variable{{Name: "V", Default: "24"}, {Name: "CMD"}},
	}
	out, err := Render(tpl, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "FROM node:24\n" {
		t.Fatalf("got %q", out)
	}
	out, _ = Render(tpl, map[string]string{"V": "22", "CMD": "make", "UNDECLARED": "x"})
	if out != "FROM node:22\nRUN make" {
		t.Fatalf("got %q", out)
	}
}

func TestRenderUndeclaredVariableFails(t *testing.T) {
	tpl := &store.Template{Name: "t", Dockerfile: "FROM {{ .MISSING }}"}
	if _, err := Render(tpl, nil); err == nil {
		t.Fatal("expected an error for an undeclared variable")
	}
}

func TestValidate(t *testing.T) {
	bad := []*store.Template{
		{Name: "", Dockerfile: "FROM x"},
		{Name: "t", Dockerfile: "FROM {{ .X "},
		{Name: "t", Dockerfile: "FROM x", Variables: []store.Variable{{Name: "1BAD"}}},
		{Name: "t", Dockerfile: "FROM x", Variables: []store.Variable{{Name: "A"}, {Name: "A"}}},
	}
	for i, tpl := range bad {
		if Validate(tpl) == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}
}

func TestBuiltinsRenderWithDefaults(t *testing.T) {
	for _, b := range Builtins {
		tpl := b
		if err := Validate(&tpl); err != nil {
			t.Errorf("%s: %v", b.ID, err)
			continue
		}
		out, err := Render(&tpl, nil)
		if err != nil {
			t.Errorf("%s: %v", b.ID, err)
		}
		if !strings.Contains(out, "FROM ") || strings.Contains(out, "{{") {
			t.Errorf("%s: unexpected output:\n%s", b.ID, out)
		}
	}
}
