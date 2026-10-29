package virtbench

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/manifest"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/safefs"
)

type manifestRequest struct {
	Prepared  bool
	TR        core.TestRequirement
	Directory string
	Args      []string
}

// Legacy aliases allow released upstream templates to use the same Go template engine.
var vmRenderer manifest.ManifestRenderer = manifest.GoTemplate{Aliases: map[string]string{
	"VM_NAME": "VM_NAME", "STORAGE_CLASS": "STORAGE_CLASS_NAME", "STORAGE_CLASS_NAME": "STORAGE_CLASS_NAME",
	"VM_PASSWORD": "VM_PASSWORD", "FIO_RUNTIME": "FIO_RUNTIME", "FIO_BS": "FIO_BS", "FIO_RW": "FIO_RW",
	"FIO_IODEPTH": "FIO_IODEPTH", "FIO_NUMJOBS": "FIO_NUMJOBS", "FIO_SIZE": "FIO_SIZE",
	"TARGET_NODE": "TARGET_NODE", "NODE_NAME": "NODE_NAME", "HOSTNAME_LABEL": "HOSTNAME_LABEL", "NAMESPACE": "NAMESPACE",
}}

type templateInput struct {
	key      string
	flag     string
	fallback any
}

// Defaults are owned here and passed explicitly where execution also needs them.
func templateInputs(command string) []templateInput {
	name := defaultVMName
	switch command {
	case "fio":
		name = defaultFIOVMName
	case "disk-ops":
		name = "disk-ops-vm"
	}
	inputs := []templateInput{
		{"VM_NAME", "--vm-name", name},
		{"STORAGE_CLASS_NAME", "--storage-class", ""},
		{"VM_PASSWORD", "--vm-password", "changeme"},
	}
	if command == "fio" {
		inputs = append(inputs,
			templateInput{"FIO_RUNTIME", "--fio-runtime", defaultFIORuntime},
			templateInput{"FIO_BS", "--fio-bs", "4k"},
			templateInput{"FIO_RW", "--fio-rw", "randrw"},
			templateInput{"FIO_IODEPTH", "--fio-iodepth", 64},
			templateInput{"FIO_NUMJOBS", "--fio-numjobs", 4},
			templateInput{"FIO_SIZE", "--fio-size", "10G"},
		)
	}
	return inputs
}

func resolveTemplateValues(command string, args []string) (map[string]any, error) {
	values := make(map[string]any)
	for _, input := range templateInputs(command) {
		value := input.fallback
		if raw, exists := argument(args, input.flag); exists {
			if _, numeric := value.(int); numeric {
				n, err := strconv.Atoi(raw)
				if err != nil || n < 1 {
					return nil, fmt.Errorf("virtbench: %s must be a positive integer", input.flag)
				}
				value = n
			} else {
				if raw == "" || strings.ContainsAny(raw, "\r\n") {
					return nil, fmt.Errorf("virtbench: %s must be a nonempty single-line value", input.flag)
				}
				value = raw
			}
		}
		values[input.key] = value
	}
	return values, nil
}

func prepareManifest(ctx context.Context, request manifestRequest) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	args := slices.Clone(request.Args)
	command := virtbenchCommand(args)
	if command != "datasource-clone" && command != "fio" && command != "disk-ops" {
		return args, nil
	}
	values, err := resolveTemplateValues(command, append(slices.Clone(args), planTemplateArgs(command, request.TR)...))
	if err != nil {
		return nil, err
	}
	for _, input := range templateInputs(command) {
		if input.key == "VM_PASSWORD" {
			continue
		}
		if _, present := argument(args, input.flag); !present {
			if value := fmt.Sprint(values[input.key]); value != "" {
				args = append(args, input.flag, value)
			}
		}
	}
	if command == "fio" {
		if action, _ := argument(args, "--action"); action != "" && action != "deploy" && action != "run-all" {
			return args, nil
		}
	}
	if values["STORAGE_CLASS_NAME"] == "" {
		return nil, fmt.Errorf("virtbench: %s requires --storage-class", command)
	}
	path, custom := argument(args, "--vm-template")
	var source []byte
	if custom {
		source, err = safefs.ReadFile(path)
	} else {
		tr := request.TR
		if tr.ID == "" {
			tr.ID = "TR-VIRT-002"
			if command == "fio" {
				tr.ID = "TR-STOR-002"
			}
			if command == "disk-ops" {
				tr.ID = "TR-STOR-001"
			}
		}
		source, err = vmSourceTemplate(ctx, tr)
		path = tr.ID + "-vm.yaml"
	}
	if err != nil {
		return nil, fmt.Errorf("virtbench: load template %s: %w", path, err)
	}
	values["TARGET_NODE"] = strParamOr(request.TR, "target_node", "")
	rendered := source
	if request.Prepared {
		if !custom {
			return nil, fmt.Errorf("prepared VM requires --vm-template")
		}
		if err := validateVMTemplate(rendered, values); err != nil {
			return nil, err
		}
	} else {
		rendered, err = renderVMTemplate(path, source, values)
		if err != nil {
			return nil, err
		}

	}
	if raw, present := argument(args, "--num-disks"); present && command == "datasource-clone" {
		wanted, err := strconv.Atoi(raw)
		if err != nil || wanted < 1 {
			return nil, fmt.Errorf("virtbench: --num-disks must be a positive integer")
		}
		got, err := manifestDiskCount(rendered)
		if err != nil {
			return nil, err
		}
		if got != wanted {
			return nil, fmt.Errorf("virtbench: --num-disks=%d but rendered VM defines %d disks", wanted, got)
		}
	}
	destination := path
	if !request.Prepared {
		dir := filepath.Join(request.Directory, "rendered")
		if custom {
			original, err := filepath.Abs(path)
			if err != nil {
				return nil, err
			}
			output, err := filepath.Abs(filepath.Join(dir, command+"-vm.yaml"))
			if err != nil {
				return nil, err
			}
			if original == output {
				return nil, fmt.Errorf("VM template is inside the rendering destination; choose a separate workdir to preserve %s", path)
			}
		}
		destination, err = writeTemplateFile(dir, command+"-vm.yaml", rendered)
		if err != nil {
			return nil, err
		}
	}

	if command == "datasource-clone" {
		args = removeArgument(args, "--storage-class")
	}
	return setArgument(args, "--vm-template", destination), nil
}

func writeTemplateFile(directory, name string, data []byte) (string, error) {
	if err := safefs.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	path, err := filepath.Abs(filepath.Join(directory, name))
	if err != nil {
		return "", err
	}
	if err := safefs.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func renderVMTemplate(name string, source []byte, values map[string]any) ([]byte, error) {
	rendered, err := manifest.Render(vmRenderer, name, source, values)
	if err != nil {
		return nil, err
	}
	if err := validateVMTemplate(rendered, values); err != nil {
		return nil, err
	}
	return rendered, nil
}

func validateVMTemplate(rendered []byte, values map[string]any) error {
	documents, err := manifest.Documents(rendered)
	if err != nil {
		return err
	}
	if len(documents) != 1 {
		return fmt.Errorf("virtbench: VM template must contain one YAML document")
	}
	count := 0
	for _, object := range documents {
		if object["kind"] != "VirtualMachine" {
			continue
		}
		count++
		if version, _ := object["apiVersion"].(string); version == "" {
			return fmt.Errorf("virtbench: VM template requires apiVersion")
		}
		metadata, _ := object["metadata"].(map[string]any)
		if name, _ := metadata["name"].(string); name == "" || name != values["VM_NAME"] {
			return fmt.Errorf("virtbench: VM metadata.name must match --vm-name")
		}
		if _, ok := object["spec"].(map[string]any); !ok {
			return fmt.Errorf("virtbench: VM template requires a spec mapping")
		}
	}
	if count != 1 {
		return fmt.Errorf("virtbench: template must contain exactly one VirtualMachine, found %d", count)
	}
	return nil
}

func runtimeTemplate(ctx context.Context, relative string) ([]byte, error) {
	for _, root := range []string{os.Getenv("VIRTBENCH_REPO"), "/opt/virtbench-runtime", ".", ".."} {
		if root == "" {
			continue
		}
		if data, err := safefs.ReadFile(filepath.Join(root, relative)); err == nil {
			return data, nil
		}
	}
	root, err := installedRuntimeRoot(ctx)
	if err == nil {
		if data, readErr := safefs.ReadFile(filepath.Join(root, relative)); readErr == nil {
			return data, nil
		} else {
			err = readErr
		}
	}
	return nil, fmt.Errorf("cannot locate released template %s; set vm_template or VIRTBENCH_REPO: %w", relative, err)
}

// installedRuntimeRoot uses the same Python environment and discovery code as the CLI.
func installedRuntimeRoot(ctx context.Context) (string, error) {
	launcher, err := exec.LookPath(binary)
	if err != nil {
		return "", err
	}
	launcher, err = filepath.EvalSymlinks(launcher)
	if err != nil {
		return "", err
	}
	data, err := safefs.ReadFile(launcher)
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(string(data), "\n")
	interpreter := strings.Fields(strings.TrimPrefix(line, "#!"))
	if len(interpreter) == 0 || !strings.HasPrefix(line, "#!") {
		return "", fmt.Errorf("virtbench launcher has no Python shebang")
	}
	if filepath.Base(interpreter[0]) == "env" {
		interpreter = interpreter[1:]
		if len(interpreter) > 0 && interpreter[0] == "-S" {
			interpreter = interpreter[1:]
		}
	}
	if len(interpreter) == 0 || !strings.HasPrefix(filepath.Base(interpreter[0]), "python") {
		return "", fmt.Errorf("virtbench launcher does not identify its Python interpreter")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	args := append(interpreter[1:], "-c", "from virtbench.common import find_repo_root; print(find_repo_root())")
	cmd := exec.CommandContext(ctx, interpreter[0], args...) // #nosec G204 -- interpreter belongs to the selected Virtbench CLI.
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("discover installed Virtbench runtime: %w", err)
	}
	root := strings.TrimSpace(string(out))
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("virtbench returned a non-absolute runtime directory")
	}
	return root, nil
}

func virtbenchCommand(args []string) string {
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "--") {
			if !strings.Contains(args[i], "=") {
				i++
			}
			continue
		}
		return args[i]
	}
	return ""
}

func argument(args []string, flag string) (string, bool) {
	for i, arg := range args {
		if value, ok := strings.CutPrefix(arg, flag+"="); ok {
			return value, true
		}
		if arg == flag {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				return args[i+1], true
			}
			return "", true
		}
	}
	return "", false
}

func setArgument(args []string, flag, value string) []string {
	for i, arg := range args {
		if strings.HasPrefix(arg, flag+"=") {
			args[i] = flag + "=" + value
			return args
		}
		if arg == flag && i+1 < len(args) {
			args[i+1] = value
			return args
		}
	}
	return append(args, flag, value)
}

func removeArgument(args []string, flag string) []string {
	result := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == flag {
			i++
		} else if !strings.HasPrefix(args[i], flag+"=") {
			result = append(result, args[i])
		}
	}
	return result
}

// vmSourceTemplate selects raw bytes; custom templates preserve their storage settings.
func vmSourceTemplate(ctx context.Context, tr core.TestRequirement) ([]byte, error) {
	sc, ok := scenarioFor(tr)
	if !ok {
		return nil, fmt.Errorf("no VM template for %s", tr.ID)
	}
	param := sc.Template.Param
	if param == "" {
		param = "vm_template"
	}
	path := strParamOr(tr, param, "")
	if param == "fio_vm_template" {
		var err error
		path, err = fioTemplate(tr)
		if err != nil {
			return nil, err
		}
	}
	if path != "" {
		data, err := safefs.ReadFile(path)
		return data, err
	}
	if sc.Template.Source != "" {
		return []byte(sc.Template.Source), nil
	}
	data, err := runtimeTemplate(ctx, sc.Template.Upstream)
	return data, err
}

// planTemplateArgs maps ordinary plan parameters to the shared input resolver.
func planTemplateArgs(command string, tr core.TestRequirement) []string {
	var args []string
	for _, input := range templateInputs(command) {
		param := strings.ReplaceAll(strings.TrimPrefix(input.flag, "--"), "-", "_")
		if value, ok := tr.Params[param]; ok {
			args = append(args, input.flag, fmt.Sprint(value))
		}
	}
	return args
}

func resolvePlanTemplateValues(command string, rc *core.RunCtx, tr core.TestRequirement) (map[string]any, error) {
	args := planTemplateArgs(command, tr)
	if sc := storageClass(rc, tr); sc != "" {
		args = setArgument(args, "--storage-class", sc)
	}
	return resolveTemplateValues(command, args)
}
