package clustercheck

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// ApplyVirtualMachine applies one VirtualMachine manifest, preserving kubectl apply semantics.
func ApplyVirtualMachine(ctx context.Context, kubeconfig string, data []byte) error {
	var resource struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name      string `yaml:"name"`
			Namespace string `yaml:"namespace"`
		} `yaml:"metadata"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&resource); err != nil {
		return fmt.Errorf("decode VirtualMachine manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("apply VirtualMachine requires exactly one YAML document")
	}
	if resource.Kind != "VirtualMachine" {
		return fmt.Errorf("apply VirtualMachine requires a VirtualMachine manifest, got %q", resource.Kind)
	}
	if err := validateResourceName("VirtualMachine", resource.Metadata.Name); err != nil {
		return err
	}
	if err := validateResourceName("namespace", resource.Metadata.Namespace); err != nil {
		return fmt.Errorf("VirtualMachine requires an explicit metadata.namespace: %w", err)
	}
	_, err := runKubeCommand(ctx, kubeconfig, data, "apply", "-f", "-")
	return err
}
