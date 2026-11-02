package clustercheck

import (
	"context"
	"encoding/json"
	"fmt"
)

// ListNamespaces returns metadata for all namespaces.
func ListNamespaces(ctx context.Context, kubeconfig string) ([]ResourceMetadata, error) {
	data, err := runKubeCommand(ctx, kubeconfig, nil, "get", "namespace", "-o", "json")
	if err != nil {
		return nil, err
	}
	var value struct {
		Items []struct {
			Metadata ResourceMetadata `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("decode namespace list: %w", err)
	}
	result := make([]ResourceMetadata, 0, len(value.Items))
	for _, item := range value.Items {
		result = append(result, item.Metadata)
	}
	return result, nil
}

// DeleteNamespace idempotently deletes only the named namespace without waiting.
func DeleteNamespace(ctx context.Context, kubeconfig, name string) error {
	if err := validateResourceName("namespace", name); err != nil {
		return err
	}
	_, err := runKubeCommand(ctx, kubeconfig, nil, "delete", "namespace", name, "--ignore-not-found", "--wait=false")
	return err
}
