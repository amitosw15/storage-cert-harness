package kubeburner

import "embed"

//go:embed templates/TR-VIRT-010/*.yaml
var templates embed.FS

const snapshotTemplatesDir = "templates/TR-VIRT-010"
