package manifest

import (
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
	data, err := (GoTemplate{}).Render([]byte(source), values)
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
			_, err := renderer.Render([]byte(tt.source), map[string]any{"value": "{{later}}"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLegacyAliasesUseExplicitValues(t *testing.T) {
	renderer := GoTemplate{Aliases: map[string]string{"NAME": "name", "CLASS": "storage"}}
	data, err := renderer.Render([]byte("name: {{ NAME }}\nclass: {{ CLASS }}"), map[string]any{"name": "vm", "storage": "fast"})
	if err != nil || string(data) != "name: vm\nclass: fast" {
		t.Fatalf("data=%s error=%v", data, err)
	}
}

func TestNativePassesThroughWithoutSharingSlice(t *testing.T) {
	source := []byte("name: {{ .Iteration }}")
	got, err := (Native{}).Render(source, map[string]any{"Iteration": 3})
	if err != nil || string(got) != string(source) {
		t.Fatalf("data=%s error=%v", got, err)
	}
	got[0] = 'X'
	if source[0] != 'n' {
		t.Fatal("native renderer shares caller bytes")
	}
}

func TestSprigFunctionsAreAvailable(t *testing.T) {
	data, err := (GoTemplate{}).Render([]byte(`name: {{ "" | default "fallback" | upper | quote }}
count: {{ add 2 3 }}
values: {{ dict "enabled" true "items" (list "a" "b") | toYaml | nindent 2 }}
`), nil)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := Documents(data)
	if err != nil {
		t.Fatal(err)
	}
	if docs[0]["name"] != "FALLBACK" || docs[0]["count"] != 5 {
		t.Fatalf("unexpected Sprig output: %s", data)
	}
}
