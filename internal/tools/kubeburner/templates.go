package kubeburner

import "embed"

//go:embed templates
var templates embed.FS

const (
	workloadTemplatesDir = "templates/shared"
)
