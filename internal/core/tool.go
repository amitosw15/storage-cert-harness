package core

// ToolName identifies the executable integration behind a test requirement.
type ToolName string

const (
	ToolExample       ToolName = "example"
	ToolManual        ToolName = "manual"
	ToolVirtbench     ToolName = "virtbench"
	ToolKubeBurner    ToolName = "kube-burner"
	ToolKubeBurnerOCP ToolName = "kube-burner-ocp"
)

// Valid reports whether the tool name is part of the catalog contract.
func (name ToolName) Valid() bool {
	switch name {
	case ToolExample, ToolManual, ToolVirtbench, ToolKubeBurner, ToolKubeBurnerOCP:
		return true
	default:
		return false
	}
}
