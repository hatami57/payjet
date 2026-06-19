// Command virtual demonstrates the core payjet flow end to end with no real
// bank, no database, and no HTTP server: it drives the virtual gateway through
// Request → (simulated user payment) → Verify and prints each step.
//
// Every gateway implements the same payjet.Gateway interface, so swapping
// virtual.New for zarinpal.New, mellat.New, … changes nothing below but the
// constructor.
//
//	go run .
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/majid/payjet"
	"github.com/majid/payjet/virtual"
)

func main() {
	ctx := context.Background()

	// The virtual gateway serves its own payment page at this URL; for a real
	// gateway you would pass merchant credentials instead.
	gw := virtual.New("http://localhost:8080/virtual-pay")

	p := &payjet.Payment{
		OrderID:     "order-1001",
		Amount:      500_000, // 50,000 Tomans, in Rials
		CallbackURL: "http://localhost:8080/payment/callback",
		Description: "خرید نمونه",
	}

	// Validate fails fast on missing fields before any round-trip.
	if err := p.Validate(); err != nil {
		log.Fatalf("invalid payment: %v", err)
	}

	// Money() bridges the int64 Rial amount to microjet's currency-aware type.
	m := p.Money()
	fmt.Printf("charging %s %s for %s\n", m.Value.String(), m.CurrencyCode, p.OrderID)

	// 1) Request — initiate the payment and get redirect info.
	req, err := gw.Request(ctx, p)
	if err != nil {
		log.Fatalf("request failed: %v", err)
	}
	fmt.Printf("redirect the user (%s) to %s (token %s)\n", req.Method, req.PaymentURL, req.Token)

	// 2) The user pays on the gateway page. SimulatePayment fakes the callback
	//    params the bank would post back — pass false to simulate a cancellation.
	params := gw.SimulatePayment(p.OrderID, true)

	// 3) Verify — confirm the payment once the user returns to CallbackURL.
	res, err := gw.Verify(ctx, p, params)
	if err != nil {
		log.Fatalf("verify failed: %v", err)
	}

	fmt.Printf("payment verified: refID=%s amount=%d card=%s\n",
		res.RefID, res.Amount, res.CardNumber)
}
