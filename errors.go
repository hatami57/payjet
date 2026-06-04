package payjet

import (
	"errors"
	"fmt"

	"github.com/hatami57/microjet/core"
)

// Sentinel errors for common, non-exceptional outcomes. Test for them with
// errors.Is so callers can branch on the kind of failure rather than parsing
// error strings. They are carried as the Inner of the *core.Error returned by
// the helpers below, so errors.Is keeps working while the error also gains a
// category that microjet's HTTP error middleware maps to a status code.
var (
	// ErrCancelled means the user cancelled, or the payment failed at the gateway
	// before completion. This is a normal "let them try again" flow, not a fault.
	ErrCancelled = errors.New("payment cancelled or failed at gateway")
	// ErrAmountMismatch means the verified amount differs from the requested amount.
	ErrAmountMismatch = errors.New("verified amount does not match requested amount")
	// ErrOrderMismatch means the order ID in the callback does not match the payment.
	ErrOrderMismatch = errors.New("callback order ID does not match payment")
)

// gatewayError builds the structured *core.Error shared by all the helpers
// below. The gateway name becomes the error Subject, and op/gatewayCode are
// attached as Params so logs and JSON responses carry them. inner, when set,
// is a sentinel (e.g. ErrCancelled) that keeps errors.Is working.
func gatewayError(typ core.ErrorType, gateway, op, code, message string, inner error) *core.Error {
	if message == "" {
		message = fmt.Sprintf("gateway %s failed", op)
	}
	e := core.NewError(typ, gateway, message).WithParams("op", op)
	if code != "" {
		e = e.WithParams("gatewayCode", code)
	}
	if inner != nil {
		e = e.WithInner(inner)
	}
	return e
}

// Declined reports a normal, retryable outcome: the user cancelled or the bank
// rejected the payment at the gateway. It is a Business error (microjet maps it
// to HTTP 409) and also matches errors.Is(err, ErrCancelled).
func Declined(gateway, op, code, message string) *core.Error {
	if message == "" {
		message = ErrCancelled.Error()
	}
	return gatewayError(core.BusinessErrorType, gateway, op, code, message, ErrCancelled)
}

// Rejected reports that the gateway returned a failure code for an otherwise
// well-formed request (e.g. invalid merchant, expired session). Business → 409.
func Rejected(gateway, op, code, message string) *core.Error {
	return gatewayError(core.BusinessErrorType, gateway, op, code, message, nil)
}

// Mismatch wraps ErrOrderMismatch or ErrAmountMismatch as a Business error so
// the caller can both branch with errors.Is and get an HTTP 409.
func Mismatch(gateway, op string, sentinel error) *core.Error {
	return gatewayError(core.BusinessErrorType, gateway, op, "", sentinel.Error(), sentinel)
}

// Fault reports an unexpected failure communicating with or parsing the gateway
// (transport errors, malformed callbacks). Internal → 500. inner may be nil.
func Fault(gateway, op, message string, inner error) *core.Error {
	return gatewayError(core.InternalErrorType, gateway, op, "", message, inner)
}

// Invalid reports a bad caller-supplied value detected by a gateway before any
// network call (e.g. a non-numeric OrderID where the gateway requires digits).
// BadRequest → 400.
func Invalid(gateway, op, message string) *core.Error {
	return gatewayError(core.BadRequestErrorType, gateway, op, "", message, nil)
}
