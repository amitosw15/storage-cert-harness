package clustercheck

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

// CollectEnvironment gathers the run environment (hardware + platform) from the
// cluster, best-effort: any datum that cannot be read is simply omitted. cli is
// "oc" or "kubectl" (see KubeCLI). scUnderTest is the StorageClass being tested
// (from the active backend); its provisioner determines the reported CSI driver.
// When empty, the cluster's default StorageClass is used. See ADR-0014.
func CollectEnvironment(ctx context.Context, cli, scUnderTest string) core.Environment {
	return core.Environment{
		Hardware: collectHardware(ctx, cli),
		Platform: collectPlatform(ctx, cli, scUnderTest),
	}
}

func collectHardware(ctx context.Context, cli string) core.Hardware {
	hw := core.Hardware{}
	var nl struct {
		Items []struct {
			Metadata struct {
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Status struct {
				Capacity map[string]string `json:"capacity"`
				NodeInfo struct {
					Architecture string `json:"architecture"`
					OSImage      string `json:"osImage"`
				} `json:"nodeInfo"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := runJSON(ctx, cli, &nl, "get", "nodes", "-o", "json"); err != nil {
		return hw
	}
	hw.Nodes = len(nl.Items)
	var arch, osImage, cpu, ram distinct
	roles := map[string]int{}
	for _, n := range nl.Items {
		arch.add(n.Status.NodeInfo.Architecture)
		osImage.add(n.Status.NodeInfo.OSImage)
		cpu.add(n.Status.Capacity["cpu"])
		ram.add(kiToGi(n.Status.Capacity["memory"]))
		for k := range n.Metadata.Labels {
			if r, ok := strings.CutPrefix(k, "node-role.kubernetes.io/"); ok && r != "" {
				roles[r]++
			}
		}
	}
	const src = "oc get nodes -o json"
	hw.Facts = facts(src,
		"arch", arch.String(),
		"os_image", osImage.String(),
		"cpu_per_node", cpu.String(),
		"ram_per_node", ram.String(),
		"node_roles", formatRoles(roles),
	)
	return hw
}

// distinct collects the unique values of one fact across nodes, in first-seen
// order, so a heterogeneous cluster is not reported as if it were uniform.
type distinct struct {
	seen   map[string]bool
	values []string
}

func (d *distinct) add(v string) {
	if v == "" || d.seen[v] {
		return
	}
	if d.seen == nil {
		d.seen = map[string]bool{}
	}
	d.seen[v] = true
	d.values = append(d.values, v)
}

func (d *distinct) String() string { return strings.Join(d.values, ", ") }

func formatRoles(roles map[string]int) string {
	names := make([]string, 0, len(roles))
	for r := range roles {
		names = append(names, r)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, r := range names {
		parts = append(parts, fmt.Sprintf("%s=%d", r, roles[r]))
	}
	return strings.Join(parts, ",")
}

func collectPlatform(ctx context.Context, cli, scUnderTest string) core.Platform {
	p := core.Platform{}

	p.OCPVersion = strings.TrimSpace(runText(ctx, cli,
		"get", "clusterversion", "version", "-o", "jsonpath={.status.desired.version}"))

	var ver struct {
		ServerVersion struct {
			GitVersion string `json:"gitVersion"`
		} `json:"serverVersion"`
	}
	if runJSON(ctx, cli, &ver, "version", "-o", "json") == nil {
		p.KubeVersion = ver.ServerVersion.GitVersion
	}

	// Operators (CSVs) cluster-wide, so the storage vendor's own operator is
	// captured and not just CNV's. OLM copies an AllNamespaces CSV into every
	// namespace, so only the originals are kept. Also yields the CNV version.
	var csvs struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Version string `json:"version"`
			} `json:"spec"`
			Status struct {
				Reason string `json:"reason"`
				Phase  string `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}
	const csvSrc = "oc get csv -A -o json"
	if runJSON(ctx, cli, &csvs, "get", "csv", "-A", "-o", "json") == nil {
		seen := map[string]bool{}
		for _, c := range csvs.Items {
			// An upgrade can leave superseded or failed CSVs behind; only the
			// installed one describes what the run actually ran against.
			if c.Status.Reason == "Copied" || c.Status.Phase != "Succeeded" {
				continue
			}
			base := strings.SplitN(c.Metadata.Name, ".v", 2)[0]
			if seen[base] {
				continue
			}
			seen[base] = true
			p.Operators = append(p.Operators, core.OperatorInfo{
				Name: base, Version: c.Spec.Version, Source: csvSrc,
			})
			if strings.Contains(base, "kubevirt-hyperconverged") {
				p.CNVVersion = c.Spec.Version
			}
		}
	}

	// StorageClasses; remember the default provisioner for the CSIDriver lookup.
	var scs struct {
		Items []struct {
			Metadata struct {
				Name        string            `json:"name"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Provisioner       string            `json:"provisioner"`
			ReclaimPolicy     string            `json:"reclaimPolicy"`
			VolumeBindingMode string            `json:"volumeBindingMode"`
			Parameters        map[string]string `json:"parameters"`
		} `json:"items"`
	}
	// Only the StorageClass under test and the cluster default are evidence; a
	// partner cluster can carry hundreds of unrelated ones. The total is kept
	// as a fact so nothing is silently lost.
	const scSrc = "oc get sc -o json"
	defaultProvisioner := ""
	testedProvisioner := ""
	if runJSON(ctx, cli, &scs, "get", "sc", "-o", "json") == nil {
		for _, s := range scs.Items {
			isDefault := s.Metadata.Annotations["storageclass.kubernetes.io/is-default-class"] == "true"
			underTest := s.Metadata.Name == scUnderTest
			if isDefault {
				defaultProvisioner = s.Provisioner
			}
			if underTest {
				testedProvisioner = s.Provisioner
			}
			if !isDefault && !underTest {
				continue
			}
			p.StorageClasses = append(p.StorageClasses, core.StorageClassInfo{
				Name:              s.Metadata.Name,
				Provisioner:       s.Provisioner,
				IsDefault:         isDefault,
				UnderTest:         underTest,
				ReclaimPolicy:     s.ReclaimPolicy,
				VolumeBindingMode: s.VolumeBindingMode,
				Parameters:        s.Parameters,
			})
		}
		p.Facts = append(p.Facts, facts(scSrc,
			"storage_classes_total", strconv.Itoa(len(scs.Items)))...)
	}

	// The driver under test is the provisioner of the StorageClass under test;
	// fall back to the cluster default when no backend SC was given (ADR-0014).
	provisioner := testedProvisioner
	if provisioner == "" {
		provisioner = defaultProvisioner
	}
	if provisioner != "" {
		var versionSrc string
		p.CSIDriver, versionSrc = collectCSIDriver(ctx, cli, provisioner)
		p.Facts = append(p.Facts, facts(versionSrc, "csi_driver_version", p.CSIDriver.Version)...)
	}
	return p
}

// collectCSIDriver reads the CSIDriver object and resolves the driver version,
// which that object does not carry. It returns the source of the version so the
// report can record how it was obtained; an empty source means unresolved.
func collectCSIDriver(ctx context.Context, cli, name string) (core.CSIDriverInfo, string) {
	info := core.CSIDriverInfo{Name: name}
	var d struct {
		Metadata struct {
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Spec struct {
			AttachRequired       *bool    `json:"attachRequired"`
			FSGroupPolicy        string   `json:"fsGroupPolicy"`
			VolumeLifecycleModes []string `json:"volumeLifecycleModes"`
			PodInfoOnMount       *bool    `json:"podInfoOnMount"`
			StorageCapacity      *bool    `json:"storageCapacity"`
			RequiresRepublish    *bool    `json:"requiresRepublish"`
			SELinuxMount         *bool    `json:"seLinuxMount"`
		} `json:"spec"`
	}
	// A failed read still leaves the DaemonSet fallback worth trying, so the
	// version does not depend on the CSIDriver object being readable.
	if runJSON(ctx, cli, &d, "get", "csidriver", name, "-o", "json") == nil {
		if d.Spec.AttachRequired != nil {
			info.AttachRequired = *d.Spec.AttachRequired
		}
		info.FSGroupPolicy = d.Spec.FSGroupPolicy
		info.VolumeLifecycleModes = d.Spec.VolumeLifecycleModes
		for _, c := range []struct {
			name string
			on   *bool
		}{
			{"podInfoOnMount", d.Spec.PodInfoOnMount},
			{"storageCapacity", d.Spec.StorageCapacity},
			{"requiresRepublish", d.Spec.RequiresRepublish},
			{"seLinuxMount", d.Spec.SELinuxMount},
		} {
			if c.on != nil && *c.on {
				info.Capabilities = append(info.Capabilities, c.name)
			}
		}
	}
	if v := versionFromLabels(d.Metadata.Labels, name); v != "" {
		info.Version = v
		return info, "oc get csidriver " + name + " -o json (label)"
	}
	if v := versionFromDaemonSet(ctx, cli, name); v != "" {
		info.Version = v
		return info, "oc get daemonsets -A -o json (driver image tag)"
	}
	return info, ""
}

// floatingTags are image tags and label values that name a moving target rather
// than a build, so they identify nothing and are rejected as a version.
var floatingTags = map[string]bool{
	"latest": true, "main": true, "master": true, "devel": true,
	"dev": true, "edge": true, "unknown": true, "nightly": true, "stable": true,
}

func usableVersion(v string) string {
	v = strings.TrimSpace(v)
	if floatingTags[strings.ToLower(v)] {
		return ""
	}
	return v
}

// versionFromLabels prefers a vendor "<product>_version" label, which some
// drivers publish, over app.kubernetes.io/version, which vendors
// often leave on a floating tag. Kubernetes' own version is not the driver's.
// A label naming the driver wins over any other version label, so a co-located
// component's version is never reported as the driver's.
func versionFromLabels(labels map[string]string, driver string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	named, other := "", ""
	for _, k := range keys {
		l := strings.ToLower(k)
		if strings.Contains(l, "k8s") || strings.Contains(l, "kube") {
			continue
		}
		if !strings.HasSuffix(l, "_version") && !strings.HasSuffix(l, "-version") {
			continue
		}
		v := usableVersion(labels[k])
		if v == "" {
			continue
		}
		if named == "" && matchesDriver(l, driver) {
			named = v
		}
		if other == "" {
			other = v
		}
	}
	if named != "" {
		return named
	}
	if other != "" {
		return other
	}
	return usableVersion(labels["app.kubernetes.io/version"])
}

// matchesDriver reports whether s carries a token identifying driver.
func matchesDriver(s, driver string) bool {
	for _, t := range driverTokens(driver) {
		if strings.Contains(s, t) {
			return true
		}
	}
	return false
}

type csiContainer struct {
	Name    string   `json:"name"`
	Image   string   `json:"image"`
	Args    []string `json:"args"`
	Command []string `json:"command"`
	Env     []struct {
		Value string `json:"value"`
	} `json:"env"`
}

// versionFromDaemonSet finds the node plugin for driver by the CSI registration
// socket path it mounts, then reads the version off the driver image tag.
// DaemonSets are scanned rather than pods: every CSI driver runs one, and the
// list stays small on a busy cluster.
func versionFromDaemonSet(ctx context.Context, cli, driver string) string {
	var ds struct {
		Items []struct {
			Spec struct {
				Template struct {
					Spec struct {
						Containers []csiContainer `json:"containers"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
		} `json:"items"`
	}
	if runJSON(ctx, cli, &ds, "get", "daemonsets", "-A", "-o", "json") != nil {
		return ""
	}
	for _, d := range ds.Items {
		cs := d.Spec.Template.Spec.Containers
		if !registers(cs, driver) {
			continue
		}
		if v := imageTag(driverImage(cs, driver)); v != "" {
			return v
		}
	}
	return ""
}

// registers reports whether any container names the driver's kubelet plugin
// socket directory, in an argument, a command or an environment variable. The
// leading directory is not required: manifests commonly assemble the path from
// a "$(PLUGIN_DIR)" reference this collector cannot expand.
func registers(cs []csiContainer, driver string) bool {
	socket := "/" + driver + "/"
	for _, c := range cs {
		parts := append(append([]string{}, c.Args...), c.Command...)
		for _, e := range c.Env {
			parts = append(parts, e.Value)
		}
		if strings.Contains(strings.Join(parts, " "), socket) {
			return true
		}
	}
	return false
}

var csiSidecars = []string{
	"registrar", "provisioner", "attacher", "resizer",
	"snapshotter", "livenessprobe", "liveness-probe", "health-monitor",
}

// driverImage picks the vendor's own container out of a node plugin DaemonSet,
// which also runs upstream sidecars and sometimes unrelated companions. The
// container matching the most driver tokens wins, so a companion that happens
// to share one token does not outrank the driver by appearing first.
func driverImage(cs []csiContainer, driver string) string {
	tokens := driverTokens(driver)
	best, bestScore := "", 0
	for _, c := range cs {
		hay := strings.ToLower(c.Name + " " + c.Image)
		if isSidecar(hay) {
			continue
		}
		score := 0
		for _, t := range tokens {
			if strings.Contains(hay, t) {
				score++
			}
		}
		if score > bestScore {
			best, bestScore = c.Image, score
		}
	}
	return best
}

func isSidecar(hay string) bool {
	if strings.Contains(hay, "sig-storage") {
		return true
	}
	for _, s := range csiSidecars {
		if strings.Contains(hay, s) {
			return true
		}
	}
	return false
}

// genericTokens carry no vendor identity, so matching on them would pick the
// wrong container out of a DaemonSet.
var genericTokens = map[string]bool{
	"csi": true, "com": true, "net": true, "org": true,
	"k8s": true, "driver": true, "storage": true, "kubernetes": true,
}

func driverTokens(driver string) []string {
	var out []string
	split := func(r rune) bool { return r == '.' || r == '-' }
	for _, t := range strings.FieldsFunc(strings.ToLower(driver), split) {
		if len(t) > 2 && !genericTokens[t] {
			out = append(out, t)
		}
	}
	return out
}

// imageTag returns the tag of an image reference. A name:tag@digest reference
// keeps its readable tag; a digest-only one has no version. A registry port is
// not a tag.
func imageTag(image string) string {
	if d := strings.LastIndex(image, "@"); d >= 0 {
		image = image[:d]
	}
	i := strings.LastIndex(image, ":")
	if i < 0 || strings.Contains(image[i+1:], "/") {
		return ""
	}
	return usableVersion(image[i+1:])
}

// ---- small helpers ----

func runText(ctx context.Context, cli string, args ...string) string {
	out, err := exec.CommandContext(ctx, cli, args...).Output() // #nosec G204 -- CLI is selected by cluster detection and args are structured.
	if err != nil {
		return ""
	}
	return string(out)
}

func runJSON(ctx context.Context, cli string, v any, args ...string) error {
	out, err := exec.CommandContext(ctx, cli, args...).Output() // #nosec G204 -- CLI is selected by cluster detection and args are structured.
	if err != nil {
		return err
	}
	return json.Unmarshal(out, v)
}

// facts builds a source-tagged Fact slice from key,value pairs, dropping empties.
func facts(source string, kv ...string) []core.Fact {
	var out []core.Fact
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == "" {
			continue
		}
		out = append(out, core.Fact{Key: kv[i], Value: kv[i+1], Source: source})
	}
	return out
}

func kiToGi(s string) string {
	s = strings.TrimSuffix(s, "Ki")
	ki, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%.0fGi", ki/1024/1024)
}
