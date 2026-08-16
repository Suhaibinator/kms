package domain

// ApplicationConfigurationCell is the current value or secret metadata for
// one environment in the application dashboard. SecretValue is never present;
// Value is populated only for parameters.
type ApplicationConfigurationCell struct {
	Environment    string
	Present        bool
	Value          string
	ContentType    string
	Version        uint64
	ClientBound    bool
	HasAccessToken bool
}

// ApplicationConfigurationRow compares one physical configuration key across
// every environment of an application.
type ApplicationConfigurationRow struct {
	Key   string
	Kind  string
	Cells map[string]ApplicationConfigurationCell
}

type ApplicationDashboard struct {
	Application  Application
	Environments []Namespace
	Rows         []ApplicationConfigurationRow
}

type ApplicationParameterWriteResult struct {
	Environment string `json:"environment"`
	Version     uint64 `json:"version"`
	Revision    uint64 `json:"revision"`
	Error       string `json:"error,omitempty"`
}
