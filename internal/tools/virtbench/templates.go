package virtbench

import "embed"

//go:embed templates
var templates embed.FS

const (
	fioVMTemplatePath             = "templates/TR-STOR-002/fio-vm-template.yaml"
	drainVMTemplatePath           = "templates/TR-VIRT-008/rhel9-vm-drain.yaml"
	twoDiskVMTemplatePath         = "templates/TR-VIRT-018/rhel9-vm-2disk.yaml"
	failureRecoveryVMTemplatePath = "templates/TR-VIRT-007/rhel9-vm-failure-recovery.yaml"
	sshPodManifestPath            = "templates/shared/ssh-pod.yaml"
	namespaceTemplatePath         = "templates/shared/namespace.yaml"
	volumeSnapshotTemplatePath    = "templates/shared/volume-snapshot.yaml"
	snapshotVMTemplatePath        = "templates/shared/snapshot-vm.yaml"
	singleNodeVMTemplatePath      = "templates/TR-VIRT-013/vm.yaml"
)
