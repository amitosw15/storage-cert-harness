package report

import "github.com/openshift-storage-cert/storage-cert-harness/internal/core"

func mergeStorageSelections(base, next []core.StorageSelection) []core.StorageSelection {
	out := append([]core.StorageSelection(nil), base...)
	for _, selection := range next {
		found := false
		for i, old := range out {
			if old.TR == selection.TR && old.Variant == selection.Variant && old.Scenario == selection.Scenario {
				out[i] = selection
				found = true
				break
			}
		}
		if !found {
			out = append(out, selection)
		}
	}
	return out
}
