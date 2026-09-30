package trading

// Outcome is the CLI's classification of a backend action status.
type Outcome string

const (
	OutcomeSuccess  Outcome = "success"  // authoritative completion
	OutcomeRejected Outcome = "rejected" // authoritative venue rejection
	OutcomeUnknown  Outcome = "unknown"  // may have executed; never retry, reconcile
)

// RiskOutcome classifies a ConfirmAction status or GetAction state for
// leverage/margin actions. Only "completed" is success. Risk actions never
// carry an OID, so an OID is never evidence of anything. "submitted",
// "unresolved" (terminal unknown) and any unrecognized value are unknown.
func RiskOutcome(status string) Outcome {
	switch status {
	case "completed":
		return OutcomeSuccess
	case "error":
		return OutcomeRejected
	}
	return OutcomeUnknown
}
