package quality

import "time"

// Fact describes evidence coverage, not a substitute value. Unknown and
// conflicting facts always carry a cause and an action that can clear them.
// Sources contain provider identifiers, never responses or credentials.
type Fact struct {
	State        string             `json:"state"`
	SourceClass  string             `json:"source_class"`
	Cause        string             `json:"cause,omitempty"`
	Action       string             `json:"action,omitempty"`
	Sources      []string           `json:"sources,omitempty"`
	Provider     string             `json:"provider,omitempty"`
	Explanation  string             `json:"explanation,omitempty"`
	ErrorCode    string             `json:"error_code,omitempty"`
	ObservedAt   *time.Time         `json:"observed_at,omitempty"`
	ValidUntil   *time.Time         `json:"valid_until,omitempty"`
	FraudScore   *int               `json:"fraud_score,omitempty"`
	Observations []FieldObservation `json:"observations,omitempty"`
}

// FieldObservation preserves the actual value and time of each competing source.
// It contains normalized fields only, never a raw provider response.
type FieldObservation struct {
	Provider   string    `json:"provider"`
	Value      any       `json:"value"`
	ObservedAt time.Time `json:"observed_at"`
	ValidUntil time.Time `json:"valid_until"`
}

func Known(source string) Fact { return Fact{State: "known", SourceClass: source} }
func Missing(source, cause, action string) Fact {
	return Fact{State: "unknown", SourceClass: source, Cause: cause, Action: action}
}
