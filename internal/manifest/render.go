package manifest

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"gopkg.in/yaml.v3"
)

// GoTemplate uses kube-burner's parse/execute approach with explicit inputs and strict missing keys.
type GoTemplate struct {
	Aliases map[string]string
}

func (r GoTemplate) Render(source []byte, values map[string]any) ([]byte, error) {
	const name = "manifest"
	functions := sprig.TxtFuncMap()
	functions["toYaml"] = func(value any) (string, error) {
		data, err := yaml.Marshal(value)
		return strings.TrimSuffix(string(data), "\n"), err
	}
	for alias, key := range r.Aliases {
		if _, exists := functions[alias]; exists {
			return nil, fmt.Errorf("manifest %s: alias conflicts with helper %s", name, alias)
		}
		functions[alias] = func() (any, error) {
			value, exists := values[key]
			if !exists {
				return nil, fmt.Errorf("missing value %s", key)
			}
			return value, nil
		}
	}
	t, err := template.New(name).Option("missingkey=error").Funcs(functions).Parse(string(source))
	if err != nil {
		return nil, fmt.Errorf("parse manifest template %s: %w", name, err)
	}
	var output bytes.Buffer
	if err := t.Execute(&output, values); err != nil {
		return nil, fmt.Errorf("render manifest template %s: %w", name, err)
	}
	if bytes.Contains(output.Bytes(), []byte("{{")) {
		return nil, fmt.Errorf("manifest %s: unresolved template delimiters", name)
	}
	if _, err := Documents(output.Bytes()); err != nil {
		return nil, fmt.Errorf("manifest %s: %w", name, err)
	}
	return output.Bytes(), nil
}

// Documents validates every YAML document without imposing a Kubernetes API schema.
func Documents(data []byte) ([]map[string]any, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var documents []map[string]any
	for {
		var object map[string]any
		err := decoder.Decode(&object)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid YAML document %d: %w", len(documents)+1, err)
		}
		if len(object) != 0 {
			documents = append(documents, object)
		}
	}
	if len(documents) == 0 {
		return nil, fmt.Errorf("manifest contains no documents")
	}
	return documents, nil
}
