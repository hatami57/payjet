package payjet

import (
	"errors"
	"fmt"
)

// Sentinel errors for common, non-exceptional outcomes. Test for them with
// errors.Is so callers can branch on the kind of failure rather than parsing
// error strings.
var (
	// ErrCancelled means the user cancelled, or the payment failed at the gateway
	// before completion. This is a normal "let them try again" flow, not a fault.
	ErrCancelled = errors.New("payment cancelled or failed at gateway")
	// ErrAmountMismatch means the verified amount differs from the requested amount.
	ErrAmountMismatch = errors.New("verified amount does not match requested amount")
	// ErrOrderMismatch means the order ID in the callback does not match the payment.
	ErrOrderMismatch = errors.New("callback order ID does not match payment")
)

// Error is a structured gateway error. It carries the gateway name, the
// operation, and the raw bank code so callers can log or branch precisely. It
// unwraps to a sentinel (e.g. ErrCancelled) when one applies, so errors.Is works.
type Error struct {
	Gateway     string // gateway name, e.g. "zarinpal"
	Op          string // "request" or "verify"
	GatewayCode string // raw status/result code from the bank, if any
	Message     string // human-readable detail
	Err         error  // wrapped sentinel or underlying error
}

func (e *Error) Error() string {
	msg := e.Gateway
	if e.Op != "" {
		msg += " " + e.Op
	}
	if e.GatewayCode != "" {
		msg += fmt.Sprintf(" failed (code %s)", e.GatewayCode)
	}
	switch {
	case e.Message != "":
		msg += ": " + e.Message
	case e.Err != nil:
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }
