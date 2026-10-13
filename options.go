package labcentral

// EngineOptions bound parallel native work within each lifecycle operation.
// They contain no hosted policy or provider-specific configuration.
type EngineOptions struct {
	MaxImageBuildConcurrency int `json:"maxImageBuildConcurrency"`
	MaxMachineConcurrency    int `json:"maxMachineConcurrency"`
	MaxCleanupConcurrency    int `json:"maxCleanupConcurrency"`
}

func (o EngineOptions) Effective() EngineOptions {
	if o.MaxImageBuildConcurrency == 0 {
		o.MaxImageBuildConcurrency = 3
	}
	if o.MaxMachineConcurrency == 0 {
		o.MaxMachineConcurrency = 4
	}
	if o.MaxCleanupConcurrency == 0 {
		o.MaxCleanupConcurrency = 4
	}
	return o
}

func (o EngineOptions) Validate() error {
	o = o.Effective()
	for _, entry := range []struct {
		name  string
		value int
	}{
		{"maxImageBuildConcurrency", o.MaxImageBuildConcurrency},
		{"maxMachineConcurrency", o.MaxMachineConcurrency},
		{"maxCleanupConcurrency", o.MaxCleanupConcurrency},
	} {
		if entry.value < 1 || entry.value > 32 {
			return &Error{Code: Invalid, Field: entry.name, Message: "must be 1–32, or zero for the default"}
		}
	}
	return nil
}
