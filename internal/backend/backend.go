// Package backend loads storage-array configurations (YAML) and resolves their
// credential references into an in-memory core.ResolvedBackend whose secret
// values are never serialized or logged. See decisions/0005.
package backend

import (
	"fmt"
	"log/slog"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/safefs"
)

// SupportedSchemaVersion is the backends-file schema this build understands.
const SupportedSchemaVersion = "1"

// Set is a named collection of backends loaded from a YAML file.
type Set struct {
	SchemaVersion string         `yaml:"schema_version"`
	Backends      []core.Backend `yaml:"backends"`
}

// Load reads and validates a backends YAML file.
func Load(path string) (*Set, error) {
	data, err := safefs.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read backends %s: %w", path, err)
	}
	var s Set
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse backends %s: %w", path, err)
	}
	if err := s.validate(); err != nil {
		return nil, fmt.Errorf("invalid backends %s: %w", path, err)
	}
	return &s, nil
}

func (s *Set) validate() error {
	if s.SchemaVersion != SupportedSchemaVersion {
		return fmt.Errorf("unsupported schema_version %q (want %q)", s.SchemaVersion, SupportedSchemaVersion)
	}
	seen := map[string]bool{}
	for i, b := range s.Backends {
		if b.Name == "" {
			return fmt.Errorf("backend #%d: missing name", i)
		}
		if seen[b.Name] {
			return fmt.Errorf("duplicate backend name %q", b.Name)
		}
		seen[b.Name] = true
		for key, ref := range b.Secrets {
			if err := validateRef(ref); err != nil {
				return fmt.Errorf("backend %q secret %q: %w", b.Name, key, err)
			}
		}
	}
	return nil
}

func validateRef(ref core.SecretRef) error {
	n := 0
	if ref.File != "" {
		n++
	}
	if ref.K8sSecret != nil {
		n++
	}
	if ref.Inline != "" {
		n++
	}
	if n == 0 {
		return fmt.Errorf("no source set (want one of file / k8s_secret / inline)")
	}
	if n > 1 {
		return fmt.Errorf("multiple sources set (want exactly one)")
	}
	if ref.K8sSecret != nil {
		k := ref.K8sSecret
		if k.Name == "" || k.Key == "" {
			return fmt.Errorf("k8s_secret requires name and key")
		}
	}
	return nil
}

// Get returns the named backend.
func (s *Set) Get(name string) (core.Backend, bool) {
	for _, b := range s.Backends {
		if b.Name == name {
			return b, true
		}
	}
	return core.Backend{}, false
}

// KubeSecretReader resolves a Kubernetes Secret key to a value. It will be
// satisfied by internal/kube once client-go is wired; until then callers pass
// nil and k8s_secret refs return a clear error.
type KubeSecretReader interface {
	ReadSecret(namespace, name, key string) (string, error)
}

// Resolve turns a Backend's credential references into concrete values held only
// in memory. inline secrets emit a dev-only warning. k8s_secret refs require a
// non-nil KubeSecretReader.
func Resolve(b core.Backend, kube KubeSecretReader, logger *slog.Logger) (*core.ResolvedBackend, error) {
	secrets := make(map[string]string, len(b.Secrets))
	for key, ref := range b.Secrets {
		v, err := resolveRef(b.Name, key, ref, kube, logger)
		if err != nil {
			return nil, err
		}
		secrets[key] = v
	}
	return core.NewResolvedBackend(b, secrets), nil
}

func resolveRef(backendName, key string, ref core.SecretRef, kube KubeSecretReader, logger *slog.Logger) (string, error) {
	switch {
	case ref.File != "":
		data, err := safefs.ReadFile(ref.File)
		if err != nil {
			return "", fmt.Errorf("backend %q secret %q: read file: %w", backendName, key, err)
		}
		return strings.TrimRight(string(data), "\r\n"), nil
	case ref.Inline != "":
		if logger != nil {
			logger.Warn("using inline backend credential (local dev only)",
				"backend", backendName, "secret", key)
		}
		return ref.Inline, nil
	case ref.K8sSecret != nil:
		if kube == nil {
			return "", fmt.Errorf("backend %q secret %q: k8s_secret resolution not yet wired (needs internal/kube)", backendName, key)
		}
		k := ref.K8sSecret
		return kube.ReadSecret(k.Namespace, k.Name, k.Key)
	default:
		return "", fmt.Errorf("backend %q secret %q: no source set", backendName, key)
	}
}
