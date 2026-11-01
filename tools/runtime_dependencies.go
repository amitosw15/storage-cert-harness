//go:build tools

package tools

// Keep vendoring reproducible until the runtime template and validation imports land.
import (
	_ "github.com/Masterminds/sprig/v3"
	_ "k8s.io/apimachinery/pkg/util/validation"
)
