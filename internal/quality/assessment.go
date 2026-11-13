package quality

// Assessment is a presentation of the canonical intel assessment, never a
// second scoring algorithm. Provider risks remain evidence, not purity values.
type Assessment struct {
	Facts         map[string]Fact `json:"facts"`
	Bands         Bands           `json:"bands"`
	State         string          `json:"state"`
	ScoreSource   string          `json:"score_source"`
	PurityScore   *int            `json:"purity_score"`
	PurityBand    string          `json:"purity_band"`
	NetworkType   string          `json:"network_type"`
	NetworkSource string          `json:"network_source"`
	Native        *bool           `json:"native"`
	Verdict       string          `json:"verdict"`
	Reasons       []string        `json:"reasons"`
	TorRoles      []string        `json:"tor_roles,omitempty"`
}
