package kubeburnerocp

import (
	"fmt"
	"strconv"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

type argType int

const (
	argString argType = iota
	argInt
	argBool
)

// argSpec declares a typed CLI flag sourced from a plan param.
type argSpec struct {
	flag     string
	param    string
	typ      argType
	optional bool
}

// globalArgs are optional kube-burner-ocp flags shared by every workload.
var globalArgs = []argSpec{
	{flag: "--qps", param: "qps", typ: argInt, optional: true},
	{flag: "--burst", param: "burst", typ: argInt, optional: true},
	{flag: "--gc", param: "gc", typ: argBool, optional: true},
	{flag: "--timeout", param: "timeout", typ: argString, optional: true},
}

// buildCLIArgs renders a workload CLI and its backend storage class.
func buildCLIArgs(p Params, storageClass string) ([]string, error) {
	spec, ok := workloadSpecs[p.Workload]
	if !ok {
		return nil, fmt.Errorf("kube-burner-ocp: unknown workload %q", p.Workload)
	}
	args := []string{spec.sub}
	for _, set := range [][]argSpec{spec.args, globalArgs} {
		for _, a := range set {
			seg, err := renderArg(p.Raw, a)
			if err != nil {
				return nil, err
			}
			args = append(args, seg...)
		}
	}
	args = append(args, "--local-indexing")
	if spec.scFlag != "" {
		if storageClass == "" {
			return nil, fmt.Errorf("kube-burner-ocp: workload %q requires storage_class (pass --backends/--backend)", p.Workload)
		}
		args = append(args, spec.scFlag, storageClass)
	}
	return args, nil
}

// renderArg validates a param and returns its CLI segment, or nil when absent.
func renderArg(raw map[string]any, a argSpec) ([]string, error) {
	v, present := raw[a.param]
	if !present {
		if a.optional {
			return nil, nil
		}
		return nil, fmt.Errorf("kube-burner-ocp: missing required param %q", a.param)
	}
	switch a.typ {
	case argInt:
		n, ok := core.AsInt(v)
		if !ok {
			return nil, fmt.Errorf("kube-burner-ocp: param %q must be an integer", a.param)
		}
		return []string{a.flag, strconv.Itoa(n)}, nil
	case argBool:
		b, ok := core.AsBool(v)
		if !ok {
			return nil, fmt.Errorf("kube-burner-ocp: param %q must be a boolean", a.param)
		}
		return []string{a.flag + "=" + strconv.FormatBool(b)}, nil
	default:
		s, ok := core.AsString(v)
		if !ok || s == "" {
			return nil, fmt.Errorf("kube-burner-ocp: param %q must be a non-empty string", a.param)
		}
		return []string{a.flag, s}, nil
	}
}
