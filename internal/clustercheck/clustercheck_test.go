package clustercheck

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

func TestCheckCapabilitiesRunsEveryRequestedCheck(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("fail=%v", fail), func(t *testing.T) {
			dir := t.TempDir()
			script := "#!/bin/sh\nexit 0\n"
			if fail {
				script = "#!/bin/sh\ncase \"$3\" in kubevirts.kubevirt.io|test-sc) echo forbidden; exit 1;; esac\n"
			}
			if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			got := CheckCapabilities(context.Background(), []Capability{KubeVirt, CDI, StorageClass("test-sc")})
			wantKubeVirt := "info"
			wantStorageClass := "info"
			if fail {
				wantKubeVirt = "error"
				wantStorageClass = "error"
			}
			assertFinding(t, got, "CRD kubevirts.kubevirt.io", wantKubeVirt)
			assertFinding(t, got, "CRD datavolumes.cdi.kubevirt.io", "info")
			assertFinding(t, got, `storage class "test-sc"`, wantStorageClass)
		})
	}
}

func TestCheckCapabilitiesHandlesMissingCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	got := CheckCapabilities(context.Background(), []Capability{KubeVirt, CDI, StorageClass("test-sc")})
	assertFinding(t, got, "kubevirts.kubevirt.io", "skip")
	assertFinding(t, got, "datavolumes.cdi.kubevirt.io", "skip")
	assertFinding(t, got, `storage class "test-sc" existence`, "skip")
	assertFinding(t, got, "neither kubectl nor oc found", "skip")

	got = CheckCapabilities(context.Background(), []Capability{StorageClass("")})
	assertFinding(t, got, "no storage_class configured", "skip")

	got = CheckCapabilities(context.Background(), nil)
	if len(got) != 0 {
		t.Fatalf("capabilities-only preflight with no requested checks = %+v, want none", got)
	}
}

func TestCheckCapabilitiesRunsAddedCapability(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	called := false
	additional := Capability{
		Name: "additional api",
		Check: func(_ context.Context, cli string) core.Finding {
			called = true
			if cli != "kubectl" {
				return core.Finding{Level: "error", Message: "unexpected CLI: " + cli}
			}
			return core.Finding{Level: "info", Message: "additional api available"}
		},
	}
	findings := CheckCapabilities(context.Background(), []Capability{additional})
	if !called {
		t.Fatal("added capability was not run")
	}
	assertFinding(t, findings, "additional api available", "info")
}

func TestCheckCapabilitiesDeduplicatesByName(t *testing.T) {
	called := 0
	capability := Capability{
		Name: "shared prerequisite",
		Check: func(_ context.Context, _ string) core.Finding {
			called++
			return core.Finding{Level: "info", Message: "shared prerequisite available"}
		},
	}
	findings := CheckCapabilities(context.Background(), []Capability{capability, capability})
	if called != 1 || len(findings) != 1 {
		t.Fatalf("called = %d, findings = %+v; want one check and finding", called, findings)
	}
}

func assertFinding(t *testing.T, findings []core.Finding, message, level string) {
	t.Helper()
	for _, finding := range findings {
		if strings.Contains(finding.Message, message) {
			if finding.Level != level {
				t.Fatalf("finding matching %q = %+v, want level %s", message, finding, level)
			}
			return
		}
	}
	t.Fatalf("no finding containing %q in %+v", message, findings)
}
