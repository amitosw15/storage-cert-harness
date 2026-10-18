// Package registry is the in-tree, compile-time registry of tool integrations.
// A tool package registers itself in its init(); cmd/harness blank-imports the
// tool packages so "add a tool" is "add a package + one blank import". See
// decisions/0003 (in-tree registry, single binary).
package registry

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/stages"
)

var (
	mu     sync.RWMutex
	tools  = map[string]stages.ToolIntegration{}
	setups = map[string]stages.SetupProvider{} // keyed by "<scope>/<key>"
)

// Register adds a tool integration. It panics on a duplicate or empty name,
// which surfaces wiring mistakes at startup rather than mid-run.
func Register(t stages.ToolIntegration) {
	mu.Lock()
	defer mu.Unlock()
	if t.Name == "" {
		panic("registry: tool integration with empty name")
	}
	if _, dup := tools[t.Name]; dup {
		panic(fmt.Sprintf("registry: duplicate tool integration %q", t.Name))
	}
	tools[t.Name] = t
}

// Get returns the integration registered under name.
func Get(name string) (stages.ToolIntegration, bool) {
	mu.RLock()
	defer mu.RUnlock()
	t, ok := tools[name]
	return t, ok
}

// ForTRID returns the integration that declares it Provides the given TR id.
// It is the fallback the orchestrator uses when a TR's free-text automation_tool
// label does not equal any registered Name (e.g. the KB labels a
// kube-burner-ocp TR "kube-burner-ocp pvc-density" while the adapter — one
// adapter, many workloads — registers as "kube-burner-ocp"). Binding by Provides
// keeps tool resolution independent of how the KB formats the label. If more than
// one integration provides the id (none do today), the lexically-first name wins,
// for determinism.
func ForTRID(id string) (stages.ToolIntegration, bool) {
	mu.RLock()
	defer mu.RUnlock()
	best := ""
	for name, t := range tools {
		for _, p := range t.Provides {
			if p == id && (best == "" || name < best) {
				best = name
			}
		}
	}
	if best == "" {
		return stages.ToolIntegration{}, false
	}
	return tools[best], true
}

// RegisterSetup adds a shared SetupProvider (run- or group-scoped). Providers are
// deduped by (scope, key): a group may be declared once and referenced by many
// tools via ToolIntegration.SetupGroups; a second registration of the same
// (scope, key) is ignored so a package can register defensively in init().
func RegisterSetup(p stages.SetupProvider) {
	info := p.SetupInfo()
	if info.Key == "" {
		panic("registry: setup provider with empty key")
	}
	mu.Lock()
	defer mu.Unlock()
	id := string(info.Scope) + "/" + info.Key
	if _, dup := setups[id]; dup {
		return
	}
	setups[id] = p
}

// SetupProvider returns the provider registered for (scope, key), if any.
func SetupProvider(scope stages.SetupScope, key string) (stages.SetupProvider, bool) {
	mu.RLock()
	defer mu.RUnlock()
	p, ok := setups[string(scope)+"/"+key]
	return p, ok
}

// RunSetupProviders returns every registered run-scoped SetupProvider, sorted by
// key for deterministic ordering.
func RunSetupProviders() []stages.SetupProvider {
	mu.RLock()
	defer mu.RUnlock()
	var out []stages.SetupProvider
	var keys []string
	for id := range setups {
		if strings.HasPrefix(id, string(stages.ScopeRun)+"/") {
			keys = append(keys, id)
		}
	}
	sort.Strings(keys)
	for _, id := range keys {
		out = append(out, setups[id])
	}
	return out
}

// Names returns the registered tool names, sorted.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(tools))
	for n := range tools {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
