# Runtime templates

The example tool runs offline and owns no runtime YAML templates.
Its sample results remain in `../testdata/`.

If runtime templates are added, use `<TR-ID>/<template>.yaml`, or
`shared/<template>.yaml` for resources shared by several TRs, and embed them
from `../templates.go`.
