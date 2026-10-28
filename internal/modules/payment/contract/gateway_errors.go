package contract

import "errors"

// ErrGatewayNotSubmitted certifies a local failure before the payment creation
// dispatch boundary. Generic config/request/auth errors do NOT imply this.
var ErrGatewayNotSubmitted = errors.New("payment creation was not submitted")

type gatewayNotSubmittedError struct{ cause error }

func (e *gatewayNotSubmittedError) Error() string        { return e.cause.Error() }
func (e *gatewayNotSubmittedError) Unwrap() error        { return e.cause }
func (e *gatewayNotSubmittedError) Is(target error) bool { return target == ErrGatewayNotSubmitted }

func GatewayNotSubmitted(err error) error {
	if err == nil || errors.Is(err, ErrGatewayNotSubmitted) {
		return err
	}
	return &gatewayNotSubmittedError{cause: err}
}

var (
	ErrGatewayConfigInvalid      = errors.New("payment provider config invalid")
	ErrGatewayRequestFailed      = errors.New("payment provider request failed")
	ErrGatewayResponseInvalid    = errors.New("payment provider response invalid")
	ErrGatewaySignatureInvalid   = errors.New("payment provider signature invalid")
	ErrGatewayAuthFailed         = errors.New("payment provider auth failed")
	ErrGatewayUnsupportedChannel = errors.New("payment channel type not supported by provider")
	ErrGatewayProviderNotFound   = errors.New("payment provider not found in registry")
)
