package domain

// ReleaseAcknowledgementResult carries the authoritative persisted state, never
// the incoming replay message. Only accepted events change effective state.
type ReleaseAcknowledgementResult struct {
	Disposition string
	Effective   ReleaseAcknowledgement
}

// ReleaseAcknowledgementUnavailableError rejects an acknowledgement whose
// activation identity is no longer available. It does not assert that the
// acknowledgement was ever valid: retention may have removed the evidence.
// A release stream reports this rejection without closing, allowing the client
// to discard that exact retained acknowledgement and keep receiving releases.
type ReleaseAcknowledgementUnavailableError struct{}

func (*ReleaseAcknowledgementUnavailableError) Error() string {
	return "release acknowledgement activation is unavailable"
}

func (*ReleaseAcknowledgementUnavailableError) Unwrap() error { return ErrFailedPrecondition }
