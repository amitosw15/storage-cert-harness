package clustercheck

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreflightReportsEveryClusterCheck(t *testing.T) {
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
			got := Preflight(context.Background(), []Capability{KubeVirt, CDI}, "test-sc")
			if len(got) != 3 {
				t.Fatalf("findings = %+v, want all 3 checks", got)
			}
			for i, f := range got {
				want := "info"
				if fail && i != 1 {
					want = "error"
				}
				if f.Level != want {
					t.Errorf("check %d = %+v, want %s", i, f, want)
				}
			}
		})
	}
}

func TestPreflightSkipsEachUnavailableClusterCheck(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	got := Preflight(context.Background(), []Capability{KubeVirt, CDI}, "test-sc")
	if len(got) != 3 {
		t.Fatalf("findings = %+v, want all 3 checks", got)
	}
	for _, f := range got {
		if f.Level != "skip" || !strings.Contains(f.Message, "neither kubectl nor oc found") {
			t.Errorf("finding = %+v, want skip with missing CLI reason", f)
		}
	}
	got = Preflight(context.Background(), nil, "")
	if len(got) != 1 || got[0].Level != "skip" || !strings.Contains(got[0].Message, "no storage_class configured") {
		t.Fatalf("findings = %+v, want skip with missing storage class reason", got)
	}
}
