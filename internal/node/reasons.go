package node

// Parse-report reason codes (WP06 §9). They are shared by the subscription
// parser and by the per-format converters in this package so that a node which
// cannot be represented is reported with a stable, machine-readable reason
// instead of being dropped silently.
const (
	// ReasonUnsupportedProtocol marks an input whose protocol Prism knows
	// but cannot import at all.
	ReasonUnsupportedProtocol = "UNSUPPORTED_PROTOCOL"
	// ReasonUnsupportedFeature marks a known protocol carrying a feature the
	// sing-box representation cannot express.
	ReasonUnsupportedFeature = "UNSUPPORTED_FEATURE"
	// ReasonEngineNotBuilt marks a node whose protocol needs an engine or a
	// build tag that is not part of this binary (WP07 fallback types).
	ReasonEngineNotBuilt = "ENGINE_NOT_BUILT"
	// ReasonInvalid marks malformed input or input missing required data.
	ReasonInvalid = "INVALID"
)
