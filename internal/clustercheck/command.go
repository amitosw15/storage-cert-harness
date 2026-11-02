package clustercheck

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrNotFound identifies a required resource that does not exist.
var ErrNotFound = errors.New("cluster resource not found")

// ErrAlreadyExists identifies a strict create collision.
var ErrAlreadyExists = errors.New("cluster resource already exists")

func runKubeCommand(ctx context.Context, kubeconfig string, input []byte, args ...string) ([]byte, error) {
	cli := KubeCLI()
	if cli == "" {
		return nil, fmt.Errorf("cluster command: neither kubectl nor oc found on PATH")
	}
	if kubeconfig != "" {
		args = append([]string{"--kubeconfig", kubeconfig}, args...)
	}
	cmd := exec.CommandContext(ctx, cli, args...) // #nosec G204 -- CLI is selected by cluster detection; arguments are structured.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if input != nil {
		cmd.Stdin = strings.NewReader(string(input))
	}
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s: %w", strings.Join(args, " "), ctx.Err())
		}
		if strings.Contains(stderr.String(), "(AlreadyExists)") {
			return nil, fmt.Errorf("%s: %w: %w: %s", strings.Join(args, " "), ErrAlreadyExists, err, strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
