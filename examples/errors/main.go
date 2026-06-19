// Command errors demonstrates how payjet reports failures: gateway errors are
// structured microjet *errorx.Error values carrying a category (which maps to an
// HTTP status), the gateway name, and the raw bank code — while the package-level
// sentinels (payjet.ErrCancelled, …) are preserved as the error's Inner so
// errors.Is still works.
//
//	go run .
package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/hatami57/microjet/core/errorx"
	"github.com/majid/payjet"
	"github.com/majid/payjet/virtual"
)

func main() {
	ctx := context.Background()
	gw := virtual.New("http://localhost:8080/virtual-pay")

	p := &payjet.Payment{
		OrderID:     "order-2002",
		Amount:      500_000,
		CallbackURL: "http://localhost:8080/payment/callback",
	}

	if _, err := gw.Request(ctx, p); err != nil {
		panic(err)
	}

	// Simulate the user cancelling at the gateway: Verify returns an error.
	params := gw.SimulatePayment(p.OrderID, false)
	_, err := gw.Verify(ctx, p, params)
	if err == nil {
		fmt.Println("unexpected: verify succeeded")
		return
	}

	// 1) Sentinels survive as the Inner error — branch on the exact cause.
	switch {
	case errors.Is(err, payjet.ErrCancelled):
		fmt.Println("flow: user cancelled or the bank declined")
	case errors.Is(err, payjet.ErrAmountMismatch):
		fmt.Println("flow: verified amount did not match the request")
	default:
		fmt.Println("flow: other failure")
	}

	// 2) The category maps straight to an HTTP status through microjet's
	//    middleware; inspect it without an HTTP layer using the errorx helpers.
	switch {
	case errorx.IsBusinessError(err):
		fmt.Println("category: Business → HTTP 409")
	case errorx.IsBadRequestError(err):
		fmt.Println("category: BadRequest → HTTP 400")
	case errorx.IsInternalError(err):
		fmt.Println("category: Internal → HTTP 500")
	}

	// 3) The structured payload carries the gateway name, message, and params
	//    (operation, bank code) for logging and reconciliation.
	if ce := errorx.GetError(err); ce != nil {
		fmt.Printf("structured: subject=%s message=%q params=%v\n",
			ce.Subject, ce.Message, ce.Params)
	}
}
