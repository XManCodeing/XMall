package httpintegration

// ExecutionErrorKind describes certainty about side effects, not retryability.
type ExecutionErrorKind string

const (
	// NotSent is used only for local failures before dispatch.
	NotSent ExecutionErrorKind = "not_sent"
	// Unknown means a request may have taken effect. Reconcile before retrying.
	Unknown ExecutionErrorKind = "unknown"
	// Rejected is reserved for an adapter with an explicit no-side-effect rejection
	// contract. Generic HTTP status/success-condition failures never imply it.
	Rejected ExecutionErrorKind = "rejected"
)

// ExecutionError deliberately excludes upstream errors, URLs and response bodies.
// Inspect Kind with errors.As; Error is safe for diagnostics.
type ExecutionError struct {
	Kind    ExecutionErrorKind
	message string
}

func (e *ExecutionError) Error() string {
	if e.message == "" {
		return "HTTP integration execution failed"
	}
	return e.message
}
