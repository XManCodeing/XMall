package contract

import "errors"

var (
	ErrNotFound            = errors.New("site connection not found")
	ErrInvalid             = errors.New("site connection is invalid")
	ErrProtocolUnsupported = errors.New("unsupported protocol for site connection")
	ErrProtocolImmutable   = errors.New("connection protocol cannot be changed")
	ErrHTTPDraftOnly       = errors.New("HTTP integration is not ready for activation")
	ErrHTTPConfigConflict  = errors.New("HTTP integration configuration revision conflict")
	ErrHTTPConfigInvalid   = errors.New("invalid HTTP integration configuration")
	ErrHTTPDebugFailed     = errors.New("HTTP integration debug failed")
)
