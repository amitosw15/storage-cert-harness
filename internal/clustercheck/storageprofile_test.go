package clustercheck

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStorageProfileWaitsForEffectiveDefaults(t *testing.T) {
	for _, initial := range []string{
		`{}`,
		`{"status":{"snapshotClass":"nondefault","claimPropertySets":[{"volumeMode":"Block"}]}}`,
		`{"status":{"snapshotClass":"wrong","claimPropertySets":[{"volumeMode":"Block","accessModes":["ReadWriteMany"]}]}}`,
	} {
		t.Run(initial, func(t *testing.T) {
			dir := snapshotCLI(t)
			t.Setenv("TEST_INITIAL_PROFILE", initial)
			t.Setenv("TEST_PROFILE", `{"status":{"snapshotClass":"nondefault","claimPropertySets":[{"volumeMode":"Filesystem","accessModes":["ReadWriteOnce"]}]}}`)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := WaitForStorageProfile(ctx, SnapshotSelection{StorageClass: "backend", SnapshotClass: "nondefault", Kubeconfig: "/test/kubeconfig"}, "storage-cert-snapshot-test"); err != nil {
				t.Fatal(err)
			}
			commands, err := os.ReadFile(filepath.Join(dir, "commands"))
			if err != nil {
				t.Fatal(err)
			}
			for line := range strings.Lines(string(commands)) {
				if strings.TrimSpace(line) != "--kubeconfig /test/kubeconfig get storageprofile storage-cert-snapshot-test -o json" {
					t.Fatalf("readiness check used an unexpected command: %s", line)
				}
			}
			if strings.Count(string(commands), "get storageprofile storage-cert-snapshot-test") < 2 {
				t.Fatal("did not wait for the private profile's effective status")
			}
		})
	}
}

func TestIncompleteStorageProfileReportsMissingFields(t *testing.T) {
	for _, tc := range []struct{ profile, missing string }{
		{`{}`, "volumeMode and accessModes"},
		{`{"status":{"snapshotClass":"nondefault","claimPropertySets":[{"volumeMode":"Block"}]}}`, "claimPropertySets[0].accessModes"},
		{`{"status":{"snapshotClass":"nondefault","claimPropertySets":[{"accessModes":["ReadWriteOnce"]}]}}`, "claimPropertySets[0].volumeMode"},
		{`{"status":{"snapshotClass":"nondefault","claimPropertySets":[{"volumeMode":"Block","accessModes":[]}]}}`, "claimPropertySets[0].accessModes"},
		{`{"status":{"snapshotClass":"nondefault","claimPropertySets":[{"volumeMode":"Block","accessModes":[""]}]}}`, "empty status.claimPropertySets[0].accessModes entry"},
		{`{"status":{"snapshotClass":"nondefault","claimPropertySets":[{"volumeMode":"Block","accessModes":["ReadWriteMany"]},{}]}}`, "claimPropertySets[1].volumeMode"},
		{`{"status":{"snapshotClass":"wrong","claimPropertySets":[{"volumeMode":"Block","accessModes":["ReadWriteMany"]}]}}`, `expected "nondefault", observed "wrong"`},
	} {
		t.Run(tc.missing, func(t *testing.T) {
			snapshotCLI(t)
			t.Setenv("TEST_PROFILE", tc.profile)
			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()
			err := WaitForStorageProfile(ctx, SnapshotSelection{StorageClass: "backend", SnapshotClass: "nondefault"}, "storage-cert-snapshot-test")
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("error = %v, want readiness deadline", err)
			}
			for _, want := range []string{"storage-cert-snapshot-test", "backend", tc.missing} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q is missing %q", err, want)
				}
			}
		})
	}
}
