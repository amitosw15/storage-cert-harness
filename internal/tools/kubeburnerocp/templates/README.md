# Runtime templates

This adapter passes plan values to the released kube-burner-ocp CLI.
The CLI owns its workload templates; the harness has no local YAML templates.

If harness-owned templates are added, use `<TR-ID>/<template>.yaml`, or
`shared/<template>.yaml` for resources shared by several TRs, and embed them
from `../templates.go`.
