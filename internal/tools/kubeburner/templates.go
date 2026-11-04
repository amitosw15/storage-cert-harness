package kubeburner

import "embed"

//go:embed templates
var templates embed.FS

const (
	workloadTemplatesDir            = "templates/shared"
	storageClassTemplatePath        = "templates/shared/setup/storageclass.yaml"
	storageProfilePatchTemplatePath = "templates/shared/setup/storageprofile-patch.yaml"
)
