package virtbench

import _ "embed"

//go:embed templates/TR-STOR-002/fio-vm-template.yaml
var fioVMTemplate string

//go:embed templates/TR-VIRT-008/rhel9-vm-drain.yaml
var drainVMTemplate string

//go:embed templates/TR-VIRT-018/rhel9-vm-2disk.yaml
var twoDiskVMTemplate string

//go:embed templates/TR-VIRT-007/rhel9-vm-failure-recovery.yaml
var failureRecoveryVMTemplate string

//go:embed templates/shared/ssh-pod.yaml
var sshPodManifest string
