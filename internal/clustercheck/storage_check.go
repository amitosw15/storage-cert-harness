package clustercheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

const virtDefaultAnnotation = "storageclass.kubevirt.io/is-default-virt-class"
const kubeDefaultAnnotation = "storageclass.kubernetes.io/is-default-class"

var errStorageSourceNotReady = errors.New("storage source is not ready")

// CheckStorage checks prepared disk requests using read-only cluster operations.
func CheckStorage(ctx context.Context, kubeconfig string, requests []StorageRequest) []core.Finding {
	if len(requests) == 0 {
		return nil
	}
	classes, err := ListStorageClasses(ctx, kubeconfig)
	if err != nil {
		return []core.Finding{{Level: "warn", Message: "storage preflight incomplete: " + err.Error()}}
	}
	byName := map[string]StorageClass{}
	for _, class := range classes {
		byName[class.Metadata.Name] = class
	}
	var findings []core.Finding
	profiles := map[string]preflightStorageProfile{}
	profileErrors := map[string]error{}
	sourceClasses := map[string]storageSource{}
	authorizations := map[string]core.Finding{}
	sourceErrors := map[string]error{}
	var scratchConfig CDIConfig
	var scratchErr error
	scratchLoaded := false
	seen := map[string]bool{}
	for _, request := range requests {
		key, _ := json.Marshal(request)
		if seen[string(key)] {
			continue
		}
		seen[string(key)] = true
		emit := func(level, message string) {
			findings = append(findings, core.Finding{Level: level, Message: request.Identity + ": " + message})
		}
		target, reason := storageTarget(request, classes)
		if target == "" {
			if request.ExpectedClass != "" {
				emit("error", fmt.Sprintf("no target StorageClass selected (%s); backend requires %q", reason, request.ExpectedClass))
			} else {
				emit("warn", "no target StorageClass selected; static PV binding is not checked")
			}
			continue
		}
		if _, exists := byName[target]; !exists {
			emit("error", fmt.Sprintf("target StorageClass %q does not exist", target))
			continue
		}
		if request.ExpectedClass != "" && target != request.ExpectedClass {
			emit("error", fmt.Sprintf("target StorageClass %q (%s) differs from selected backend %q; set storageClassName on the disk", target, reason, request.ExpectedClass))
		} else {
			emit("info", fmt.Sprintf("target StorageClass=%s (%s)", target, reason))
		}
		claim := request.Claim
		if request.CDIStorage && request.ContentType == "archive" {
			if claim.VolumeMode == "Block" {
				emit("error", "archive content cannot use Block volumeMode")
				continue
			}
			claim.VolumeMode = "Filesystem"
		}
		claimResolved := true
		if request.CDIStorage && (claim.VolumeMode == "" || len(claim.AccessModes) == 0) {
			profile, loaded := profiles[target]
			profileErr, failed := profileErrors[target]
			if !loaded && !failed {
				profile, profileErr = readPreflightStorageProfile(ctx, kubeconfig, target)
				if profileErr == nil {
					profiles[target] = profile
				} else {
					profileErrors[target] = profileErr
				}
			}
			if profileErr != nil {
				claimResolved = false
				level := "warn"
				if errors.Is(profileErr, errPreflightNotFound) {
					level = "error"
				}
				emit(level, "cannot resolve omitted disk properties: "+profileErr.Error())
			} else {
				claim, err = defaultStorageClaim(claim, profile)
				if err != nil {
					claimResolved = false
					emit("error", fmt.Sprintf("StorageProfile %q: %v", target, err))
				} else {
					emit("info", fmt.Sprintf("CDI defaults: accessModes=%s volumeMode=%s", strings.Join(claim.AccessModes, ","), claim.VolumeMode))
				}
			}
		}
		if err := validateStorageClaim(claim); claimResolved && err != nil {
			emit("error", err.Error())
		}
		if request.SourceKind != "" {
			key := request.SourceKind + "/" + request.Source.Namespace + "/" + request.Source.Name
			source, loaded := sourceClasses[key]
			sourceErr, failed := sourceErrors[key]
			if !loaded && !failed {
				source, sourceErr = inspectStorageSource(ctx, kubeconfig, request.SourceKind, request.Source)
				if sourceErr != nil {
					sourceErrors[key] = sourceErr
				} else {
					sourceClasses[key] = source
				}
			}
			if sourceErr != nil {
				level := "warn"
				if errors.Is(sourceErr, errPreflightNotFound) || errors.Is(sourceErr, errStorageSourceNotReady) {
					level = "error"
				}
				emit(level, "boot source validation failed: "+sourceErr.Error())
			} else if source.Class == "" {
				emit("warn", "source exists and is ready; original StorageClass is unavailable; source/target class compatibility could not be assessed; continuing")
			} else if source.Class != target {
				emit("warn", fmt.Sprintf("source StorageClass %q differs from target StorageClass %q; there may be a compatibility or performance issue; cloning might not be accelerated and could require host-assisted copying; continuing", source.Class, target))
			} else {
				emit("info", "source StorageClass="+source.Class+" matches target; acceleration is not verified")
			}
			if sourceErr == nil {
				for _, note := range source.Notes {
					emit("info", note)
				}
				if source.Driver != "" {
					if source.Driver != byName[target].Provisioner {
						level := "warn"
						if !request.CDI {
							level = "error"
						}
						emit(level, fmt.Sprintf("snapshot driver %q differs from target provisioner %q; direct snapshot restore is incompatible and CDI may require host-assisted copying", source.Driver, byName[target].Provisioner))
					} else {
						emit("info", "snapshot driver matches target provisioner; backend restore compatibility is not verified")
					}
				}
				if source.VolumeMode != "" {
					emit("info", "source volumeMode="+source.VolumeMode)
				}
				if request.CDI {
					authKey := source.Kind + "/" + source.Reference.Namespace + "/" + source.Reference.Name
					if source.Reference.Namespace == request.Namespace {
						authKey += "/same-namespace"
					}
					if request.Namespace == "" {
						authKey += "/unresolved-target"
					}
					finding, checked := authorizations[authKey]
					if !checked {
						allowed, authErr := checkCDICloneAccess(ctx, kubeconfig, request.Namespace, source)
						finding = core.Finding{Level: "info", Message: "CDI clone authorization permitted for current kubeconfig identity"}
						if authErr != nil {
							finding = core.Finding{Level: "warn", Message: "CDI clone authorization could not be verified: " + authErr.Error()}
						} else if !allowed {
							finding = core.Finding{Level: "error", Message: fmt.Sprintf("current kubeconfig identity lacks CDI clone authorization in source namespace %q for %s %q", source.Reference.Namespace, source.Kind, source.Reference.Name)}
						}
						authorizations[authKey] = finding
					}
					emit(finding.Level, finding.Message)
				} else {
					emit("warn", "native PVC source authorization is not covered by CDI permission checks")
				}
			}
		}
		if request.ScratchRequired {
			if !scratchLoaded {
				scratchConfig, scratchErr = GetCDIConfig(ctx, kubeconfig)
				scratchLoaded = true
			}
			if scratchErr != nil {
				emit("warn", "scratch selection incomplete: "+scratchErr.Error())
				continue
			}
			scratch := scratchConfig.Status.ScratchSpaceStorageClass
			if scratch == "" {
				scratch = target
			}
			if _, exists := byName[scratch]; !exists {
				emit("error", fmt.Sprintf("required CDI scratch StorageClass %q does not exist", scratch))
			} else {
				emit("info", fmt.Sprintf("CDI scratch StorageClass=%s; requires Filesystem + ReadWriteOnce; provisioning capability is not verified", scratch))
			}
		}
	}
	return findings
}

func storageTarget(request StorageRequest, classes []StorageClass) (string, string) {
	if request.Claim.StorageClassName != nil {
		return *request.Claim.StorageClassName, "explicit storageClassName"
	}
	annotations := []string{kubeDefaultAnnotation}
	if request.CDIStorage && request.ContentType != "archive" {
		annotations = append([]string{virtDefaultAnnotation}, annotations...)
	}
	for _, annotation := range annotations {
		var matches []StorageClass
		for _, class := range classes {
			if strings.EqualFold(class.Metadata.Annotations[annotation], "true") || (annotation == kubeDefaultAnnotation && strings.EqualFold(class.Metadata.Annotations["storageclass.beta.kubernetes.io/is-default-class"], "true")) {
				matches = append(matches, class)
			}
		}
		if len(matches) > 0 {
			// Match CDI: newest first, then name ascending: https://github.com/kubevirt/containerized-data-importer/blob/eec6bfc8000c10452ff73e2704029c48791ec7a7/pkg/controller/common/util.go#L574-L602
			slices.SortFunc(matches, func(a, b StorageClass) int {
				if order := b.Metadata.CreationTimestamp.Compare(a.Metadata.CreationTimestamp); order != 0 {
					return order
				}
				return strings.Compare(a.Metadata.Name, b.Metadata.Name)
			})
			return matches[0].Metadata.Name, annotation
		}
	}
	return "", "no default StorageClass"
}

// defaultStorageClaim follows CDI's ordered profile matching and Filesystem fallback.
func defaultStorageClaim(claim StorageClaim, profile preflightStorageProfile) (StorageClaim, error) {
	var sets []claimPropertySet
	for _, raw := range profile.Status.ClaimPropertySets {
		var set claimPropertySet
		if err := json.Unmarshal(raw, &set); err != nil {
			return claim, fmt.Errorf("decode claimPropertySets: %w", err)
		}
		sets = append(sets, set)
	}
	switch {
	case len(claim.AccessModes) == 0 && claim.VolumeMode == "":
		if len(sets) == 0 || len(sets[0].AccessModes) == 0 {
			return claim, fmt.Errorf("no accessModes available to fill omitted accessModes and volumeMode")
		}
		claim.AccessModes = slices.Clone(sets[0].AccessModes)
		claim.VolumeMode = sets[0].VolumeMode
	case len(claim.AccessModes) == 0:
		for _, set := range sets {
			if set.VolumeMode == claim.VolumeMode && len(set.AccessModes) > 0 {
				claim.AccessModes = slices.Clone(set.AccessModes)
				break
			}
		}
		if len(claim.AccessModes) == 0 {
			return claim, fmt.Errorf("no matching accessModes for requested volumeMode=%s", claim.VolumeMode)
		}
	case claim.VolumeMode == "":
		found := false
		for _, set := range sets {
			for _, access := range set.AccessModes {
				if slices.Contains(claim.AccessModes, access) {
					claim.VolumeMode = set.VolumeMode
					found = true
					break
				}
			}
			if found {
				break
			}
		}
	}
	if claim.VolumeMode == "" {
		claim.VolumeMode = "Filesystem"
	}
	return claim, nil
}

func validateStorageClaim(claim StorageClaim) error {
	if claim.VolumeMode != "" && claim.VolumeMode != "Filesystem" && claim.VolumeMode != "Block" {
		return fmt.Errorf("invalid volumeMode %q", claim.VolumeMode)
	}
	if len(claim.AccessModes) == 0 {
		return fmt.Errorf("accessModes are missing and could not be resolved")
	}
	for _, mode := range claim.AccessModes {
		if !slices.Contains([]string{"ReadWriteOnce", "ReadWriteMany", "ReadOnlyMany", "ReadWriteOncePod"}, mode) {
			return fmt.Errorf("invalid accessMode %q", mode)
		}
	}
	return nil
}
