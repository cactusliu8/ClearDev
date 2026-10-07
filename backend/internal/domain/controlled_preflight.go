package domain

// ControlledPreflightEvidence separates observations from unknown account
// information. Local OpenCode configuration is not a remote service/credit test.
// These values contain no credentials, endpoints, or raw provider responses.
type ControlledPreflightEvidence struct {
	Scope          string `json:"scope"`
	Installation   string `json:"installation"`
	Configuration  string `json:"configuration"`
	Model          string `json:"model"`
	Authentication string `json:"authentication"`
	Service        string `json:"service"`
	Quota          string `json:"quota"`
}
