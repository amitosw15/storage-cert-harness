package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

// GoTemplate uses kube-burner's parse/execute approach with explicit inputs and strict missing keys.
type GoTemplate struct {
	Aliases map[string]string
}

func (r GoTemplate) Render(name string, source []byte, values map[string]any) ([]byte, error) {
	functions := template.FuncMap{
		"quote": func(value string) (string, error) {
			data, err := json.Marshal(value)
			return string(data), err
		},
		"toYaml": func(value any) (string, error) {
			data, err := yaml.Marshal(value)
			return strings.TrimSuffix(string(data), "\n"), err
		},
		"toJson": func(value any) (string, error) {
			data, err := json.Marshal(value)
			return string(data), err
		},
		"fromJson": func(value string) (any, error) {
			var result any
			err := json.Unmarshal([]byte(value), &result)
			return result, err
		},
		"indent": indent,
		"nindent": func(width int, value string) (string, error) {
			text, err := indent(width, value)
			return "\n" + text, err
		},
		"omit": omit,
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

func indent(width int, value string) (string, error) {
	if width < 0 || width > 256 {
		return "", fmt.Errorf("indent width must be between 0 and 256")
	}
	padding := strings.Repeat(" ", width)
	return padding + strings.ReplaceAll(value, "\n", "\n"+padding), nil
}

func omit(object map[string]any, keys ...string) map[string]any {
	result := make(map[string]any, len(object))
	for key, value := range object {
		result[key] = value
	}
	for _, key := range keys {
		delete(result, key)
	}
	return result
}
