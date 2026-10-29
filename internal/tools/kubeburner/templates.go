package kubeburner

import "embed"

//go:embed templates/TR-VIRT-010/*.yaml
var templates embed.FS

const snapshotTemplatesDir = "templates/TR-VIRT-010"

//go:embed templates/TR-VIRT-010/setup/storageclass.yaml
var storageClassTemplate string

//go:embed templates/TR-VIRT-010/setup/storageprofile-patch.yaml
var storageProfilePatchTemplate string
