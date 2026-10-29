// Package manifest renders runtime templates independently of tool execution.
package manifest

import (
	"bytes"
	"fmt"
)

// ManifestRenderer transforms template bytes using explicitly resolved values.
type ManifestRenderer interface {
	Render(source []byte, values map[string]any) ([]byte, error)
}

// Native leaves rendering to the wrapped tool.
type Native struct{}

func (Native) Render(source []byte, _ map[string]any) ([]byte, error) {
	return bytes.Clone(source), nil
}

var _ ManifestRenderer = Native{}
var _ ManifestRenderer = GoTemplate{}

// Render adds source context to renderer errors.
func Render(renderer ManifestRenderer, name string, source []byte, values map[string]any) ([]byte, error) {
	data, err := renderer.Render(source, values)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return data, nil
}
