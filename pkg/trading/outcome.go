package trading

// Outcome is the CLI's classification of a backend action status.
type Outcome string

const (
	OutcomeSuccess  Outcome = "success"  // authoritative completion
	OutcomeRejected Outcome = "rejected" // failed without reaching the venue
	OutcomeUnknown  Outcome = "unknown"  // may have executed; never retry, reconcile, gate conflicting exposure
)

// RiskOutcome classifies a ConfirmAction status or GetAction state for
// leverage/margin actions. Only "completed" is success. Risk actions never
// carry an OID, so an OID is never evidence.
//
// Backend "error" can be a build/validation/workflow failure rather than a
// venue rejection, so it clears the unknown state only when sentToVenue is
// false (ConfirmAction was never submitted). Once a signed action may have
// been submitted, "error" stays unknown until reconciliation yields venue
// proof. "unresolved" is a terminal workflow state, not exchange evidence,
// and stays unknown like every other value.
func RiskOutcome(status string, sentToVenue bool) Outcome {
	switch {
	case status == "completed":
		return OutcomeSuccess
	case status == "error" && !sentToVenue:
		return OutcomeRejected
	}
	return OutcomeUnknown
}
