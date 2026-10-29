package manifest

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestRenderTypedValuesAndControlFlow(t *testing.T) {
	values := map[string]any{
		"name": "colon: quote\" newline\nnext", "enabled": true,
		"items": []string{"one", "two"}, "nested": map[string]any{"count": 3, "remove": true},
	}
	source := `name: {{ .name | quote }}
{{ if .enabled }}enabled: true{{ end }}
items:
{{ range .items }}  - {{ . | quote }}
{{ end }}nested: {{ omit .nested "remove" | toYaml | nindent 2 }}
json: {{ .nested | toJson }}
`
	data, err := (GoTemplate{}).Render("typed", []byte(source), values)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := Documents(data)
	if err != nil {
		t.Fatal(err)
	}
	got := docs[0]
	if got["name"] != values["name"] || got["enabled"] != true || !reflect.DeepEqual(got["items"], []any{"one", "two"}) || !reflect.DeepEqual(got["nested"], map[string]any{"count": 3}) {
		t.Fatalf("rendered values changed: %#v", got)
	}
	if _, ok := values["nested"].(map[string]any)["remove"]; !ok {
		t.Fatal("omit mutated its input")
	}
}

func TestRenderRejectsInvalidInput(t *testing.T) {
	for _, tt := range []struct{ name, source, want string }{
		{"missing key", "name: {{ .missing }}", "map has no entry"},
		{"missing alias", "name: {{ LEGACY }}", "missing value"},
		{"unknown function", "name: {{ UNKNOWN }}", "not defined"},
		{"syntax", "name: {{", "unclosed action"},
		{"bad second document", "name: ok\n---\nname: [", "document 2"},
		{"duplicate", "name: first\nname: second", "already defined"},
		{"scalar", "plain scalar", "invalid YAML"},
		{"empty", "# comment", "no documents"},
		{"unresolved", "name: {{ .value | quote }}", "unresolved template"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			renderer := GoTemplate{Aliases: map[string]string{"LEGACY": "missing"}}
			_, err := renderer.Render(tt.name, []byte(tt.source), map[string]any{"value": "{{later}}"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLegacyAliasesUseExplicitValues(t *testing.T) {
	renderer := GoTemplate{Aliases: map[string]string{"NAME": "name", "CLASS": "storage"}}
	data, err := renderer.Render("legacy", []byte("name: {{ NAME }}\nclass: {{ CLASS }}"), map[string]any{"name": "vm", "storage": "fast"})
	if err != nil || string(data) != "name: vm\nclass: fast" {
		t.Fatalf("data=%s error=%v", data, err)
	}
}

func TestNativePassesThroughWithoutSharingSlice(t *testing.T) {
	args := []string{"init", "-c", "config-{{.Iteration}}.yaml"}
	got, err := (Native{}).Prepare(t.Context(), Request{Args: args})
	if err != nil || !reflect.DeepEqual(got, args) {
		t.Fatalf("args=%v error=%v", got, err)
	}
	got[0] = "changed"
	if args[0] != "init" {
		t.Fatal("native adapter mutated caller arguments")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (Native{}).Prepare(ctx, Request{Args: args}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}
