package clustercheck

import (
	"context"
	"encoding/json"
	"fmt"
)

// ReviewOwnAccess queries the current kubeconfig identity without persisting a resource.
func ReviewOwnAccess(ctx context.Context, kubeconfig string, attributes ResourceAccess) (bool, error) {
	review := ownAccessReview{APIVersion: "authorization.k8s.io/v1", Kind: "SelfSubjectAccessReview"}
	review.Spec.ResourceAttributes = attributes
	input, err := json.Marshal(review)
	if err != nil {
		return false, err
	}
	data, err := runKubeCommand(ctx, kubeconfig, input, "create", "--raw", "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews", "-f", "-")
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, &review); err != nil {
		return false, fmt.Errorf("decode access review: %w", err)
	}
	if review.Status == nil {
		return false, fmt.Errorf("access review returned no status")
	}
	if review.Status.Allowed {
		return true, nil
	}
	if review.Status.EvaluationError != "" {
		return false, fmt.Errorf("access review incomplete: %s", review.Status.EvaluationError)
	}
	return false, nil
}

// checkCDICloneAccess mirrors CDI's authorize_utils.go rules for the submitting identity.
func checkCDICloneAccess(ctx context.Context, kubeconfig, targetNamespace string, source storageSource) (bool, error) {
	if targetNamespace == "" || source.Reference.Namespace == "" {
		return false, fmt.Errorf("source or target namespace is unresolved")
	}
	if targetNamespace == source.Reference.Namespace {
		return true, nil
	}
	attr := ResourceAccess{Namespace: source.Reference.Namespace, Name: source.Reference.Name, Verb: "create", Group: "cdi.kubevirt.io", Resource: "datavolumes", Subresource: "source"}
	allowed, explicitErr := ReviewOwnAccess(ctx, kubeconfig, attr)
	if allowed {
		return true, nil
	}
	attr.Group = ""
	attr.Resource = "pods"
	attr.Subresource = ""
	pods, podErr := ReviewOwnAccess(ctx, kubeconfig, attr)
	if source.Kind == "pvc" {
		if pods {
			return true, nil
		}
		if explicitErr != nil {
			return false, explicitErr
		}
		return false, podErr
	}
	if !pods {
		if explicitErr != nil {
			return false, explicitErr
		}
		return false, podErr
	}
	attr.Resource = "pvcs"
	pvcs, pvcErr := ReviewOwnAccess(ctx, kubeconfig, attr)
	if pvcs {
		return true, nil
	}
	if explicitErr != nil {
		return false, explicitErr
	}
	return false, pvcErr
}

// ResourceAccess identifies one namespace-scoped permission query.
type ResourceAccess struct {
	Namespace   string `json:"namespace"`
	Name        string `json:"name,omitempty"`
	Verb        string `json:"verb"`
	Group       string `json:"group,omitempty"`
	Resource    string `json:"resource"`
	Subresource string `json:"subresource,omitempty"`
}

type ownAccessReview struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Spec       struct {
		ResourceAttributes ResourceAccess `json:"resourceAttributes"`
	} `json:"spec"`
	Status *struct {
		Allowed         bool   `json:"allowed"`
		EvaluationError string `json:"evaluationError"`
	} `json:"status,omitempty"`
}
