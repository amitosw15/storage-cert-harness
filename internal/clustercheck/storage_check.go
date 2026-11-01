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
		level := "warn"
		for _, request := range requests {
			if request.RequireSameSourceClass && request.SourceKind != "" {
				level = "error"
				break
			}
		}
		return []core.Finding{{Level: level, Message: "storage preflight incomplete: " + err.Error()}}
	}
	byName := map[string]StorageClass{}
	for _, class := range classes {
		byName[class.Metadata.Name] = class
	}
	var findings []core.Finding
	profiles := map[string]StorageProfile{}
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
		target, reason, err := storageTarget(request, classes)
		if err != nil {
			level := "warn"
			if request.RequireSameSourceClass && request.SourceKind != "" {
				level = "error"
			}
			emit(level, "target selection incomplete: "+err.Error())
			continue
		}
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
				profile, profileErr = GetStorageProfile(ctx, kubeconfig, target)
				if profileErr == nil {
					profiles[target] = profile
				} else {
					profileErrors[target] = profileErr
				}
			}
			if profileErr != nil {
				claimResolved = false
				level := "warn"
				if errors.Is(profileErr, ErrNotFound) {
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
				if request.RequireSameSourceClass || errors.Is(sourceErr, ErrNotFound) || errors.Is(sourceErr, errStorageSourceNotReady) {
					level = "error"
				}
				emit(level, "boot source validation failed: "+sourceErr.Error())
			} else if source.Class == "" {
				level := "info"
				if request.RequireSameSourceClass {
					level = "error"
				}
				emit(level, "source exists and is ready; original StorageClass is unavailable"+sourceClassPolicyReason(request))
			} else if source.Class != target {
				level := "warn"
				if request.RequireSameSourceClass {
					level = "error"
				}
				emit(level, fmt.Sprintf("source StorageClass %q differs from target StorageClass %q; cloning might not be accelerated and could require host-assisted copying%s", source.Class, target, sourceClassPolicyReason(request)))
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

func storageTarget(request StorageRequest, classes []StorageClass) (string, string, error) {
	if request.Claim.StorageClassName != nil {
		return *request.Claim.StorageClassName, "explicit storageClassName", nil
	}
	annotations := []string{kubeDefaultAnnotation}
	if request.CDIStorage && request.ContentType != "archive" {
		annotations = append([]string{virtDefaultAnnotation}, annotations...)
	}
	for _, annotation := range annotations {
		var matches []string
		for _, class := range classes {
			if strings.EqualFold(class.Metadata.Annotations[annotation], "true") || (annotation == kubeDefaultAnnotation && strings.EqualFold(class.Metadata.Annotations["storageclass.beta.kubernetes.io/is-default-class"], "true")) {
				matches = append(matches, class.Metadata.Name)
			}
		}
		if len(matches) > 1 {
			return "", "", fmt.Errorf("multiple classes have %s=true: %s", annotation, strings.Join(matches, ","))
		}
		if len(matches) == 1 {
			return matches[0], annotation, nil
		}
	}
	return "", "no default StorageClass", nil
}

// defaultStorageClaim follows CDI's ordered profile matching and Filesystem fallback.
func defaultStorageClaim(claim StorageClaim, profile StorageProfile) (StorageClaim, error) {
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

func sourceClassPolicyReason(request StorageRequest) string {
	if request.RequireSameSourceClass {
		return "; TR requires matching source and target StorageClasses (require_same_source_storage_class=true)"
	}
	return ""
}
