package clustercheck

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

// TestStoragePreflightLive exercises existing storage configurations without creating resources.
func TestStoragePreflightLive(t *testing.T) {
	kubeconfig := os.Getenv("STORAGE_PREFLIGHT_LIVE_KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("set STORAGE_PREFLIGHT_LIVE_KUBECONFIG and the live class/source variables")
	}
	required := func(key string) string {
		t.Helper()
		value := os.Getenv("STORAGE_PREFLIGHT_LIVE_" + key)
		if value == "" {
			t.Fatalf("missing live configuration %s", key)
		}
		return value
	}
	nas, economy := required("NAS_CLASS"), required("ECONOMY_CLASS")
	nasSource, economySource := required("NAS_SOURCE"), required("ECONOMY_SOURCE")
	for _, tc := range []struct {
		name, target, expected, source string
		registry                       bool
		wantError, wantWarning         bool
	}{
		{name: "NAS matching target", target: nas, expected: nas, source: nasSource},
		{name: "omitted target selects wrong default", expected: nas, source: nasSource, wantError: true},
		{name: "economy dedicated source", target: economy, expected: economy, source: economySource},
		{name: "NAS source to economy warns", target: economy, expected: economy, source: nasSource, wantWarning: true},
		{name: "missing boot source", target: nas, expected: nas, source: "openshift-virtualization-os-images/storage-preflight-missing-source", wantError: true},
		{name: "missing target", target: "storage-preflight-nonexistent", expected: "storage-preflight-nonexistent", wantError: true},
		{name: "independent scratch class", target: economy, expected: economy, registry: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			class := ""
			if tc.target != "" {
				class = fmt.Sprintf("    storageClassName: %q\n", tc.target)
			}
			source := "  source: {blank: {}}\n"
			if tc.source != "" {
				parts := strings.SplitN(tc.source, "/", 2)
				if len(parts) != 2 {
					t.Fatal("source must be namespace/name")
				}
				source = fmt.Sprintf("  sourceRef: {kind: DataSource, namespace: %q, name: %q}\n", parts[0], parts[1])
			}
			if tc.registry {
				source = "  source: {registry: {url: 'docker://example.invalid/image'}}\n"
			}
			data := []byte("apiVersion: cdi.kubevirt.io/v1beta1\nkind: DataVolume\nmetadata: {name: preflight-live}\nspec:\n" + source + "  storage:\n" + class + "    accessModes: [ReadWriteMany]\n    resources: {requests: {storage: 30Gi}}\n")
			requests, err := StorageRequests(data, tc.expected, "storage-preflight-live")
			if err != nil {
				t.Fatal(err)
			}
			tr := core.TestRequirement{ID: "live-storage", Params: map[string]any{}}
			LabelStorageRequests(requests, tr)
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			findings := CheckStorage(ctx, kubeconfig, requests)
			hasError, hasWarning := false, false
			for _, f := range findings {
				t.Logf("%s: %s", f.Level, f.Message)
				hasError = hasError || f.Level == "error"
				hasWarning = hasWarning || f.Level == "warn"
			}
			if hasError != tc.wantError || hasWarning != tc.wantWarning {
				t.Fatalf("errors=%t warnings=%t; want %t %t", hasError, hasWarning, tc.wantError, tc.wantWarning)
			}
		})
	}
}
