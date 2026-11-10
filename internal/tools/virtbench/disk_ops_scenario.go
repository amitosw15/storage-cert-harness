package virtbench

func diskOpsScenario() Scenario {
	return Scenario{
		AutomationTool: "virtbench disk-ops",
		ProvidesTR:     "TR-STOR-001",
		VMRange:        diskOpsVMRange,
		Template:       vmTemplateConfig{Upstream: "disk-ops-benchmark/vm-template.yaml"},
		ResultFile:     DiskOpsFileName,
		NSPrefix:       "disk-ops",
		BuildArgs:      diskOpsArgs("disk-ops"),
		Parse:          ParseDiskOps,
	}
}
