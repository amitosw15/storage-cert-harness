package virtbench

func fioScenario() Scenario {
	return Scenario{
		AutomationTool: "virtbench fio",
		ProvidesTR:     "TR-STOR-002",
		VMRange:        fioVMRange,
		Template:       vmTemplateConfig{Param: "fio_vm_template", Embedded: fioVMTemplatePath},
		ResultFile:     FIOFileName,
		NSPrefix:       "fio-latency",
		BuildArgs:      fioArgs("fio-latency"),
		Run:            runFIO,
		Parse:          ParseFIO,
		Collect:        collectFIOResults,
		CommandCheck:   []string{"fio", "--help"},
		Validate:       fioValidate,
	}
}
