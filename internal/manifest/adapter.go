// Package manifest prepares tool inputs and renders harness-owned manifests.
package manifest

import (
	"context"
	"slices"
)

type Request struct {
	Directory string
	Args      []string
}

// Adapter prepares manifests before a tool is executed.
type Adapter interface {
	Prepare(context.Context, Request) ([]string, error)
}

// Native leaves rendering to the wrapped tool.
type Native struct{}

func (Native) Prepare(ctx context.Context, request Request) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return slices.Clone(request.Args), nil
}
