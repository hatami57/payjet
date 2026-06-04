# payjet

A multi-gateway payment SDK for Iranian bank gateways, written in Go. Every
gateway implements one small interface, so you can swap between them without
touching your checkout or callback code. **All amounts are in Rials.**

## Supported gateways

| Package     | Constructor                                    | Notes                                            |
| ----------- | ---------------------------------------------- | ------------------------------------------------ |
| `zarinpal`  | `zarinpal.New(merchantID, ...opts)`            | REST, sandbox supported                          |
| `idpay`     | `idpay.New(apiKey, ...opts)`                   | REST, sandbox supported                          |
| `mellat`    | `mellat.New(mellat.Config{...}, ...opts)`      | SOAP, auto-settles after verify, numeric OrderID |
| `saman`     | `saman.New(terminalID, ...opts)`               | REST, POST redirect to payment page              |
| `parsian`   | `parsian.New(loginAccount, ...opts)`           | SOAP, GET redirect                               |
| `pasargad`  | `pasargad.New(pasargad.Config{...}, ...opts)`  | REST, merchant-specific base URL required        |
| `virtual`   | `virtual.New(gatewayURL, ...opts)`             | Local test gateway — no real bank                |

## Install

```sh
go get github.com/majid/payjet
```

## The interface

```go
type Gateway interface {
    Request(ctx context.Context, p *Payment) (*RequestResult, error)
    Verify(ctx context.Context, p *Payment, params map[string]string) (*VerifyResult, error)
    CallbackOrderID(params map[string]string) string
}
```

A payment flows in three steps:

1. **Request** — create a payment and get a redirect target.
2. Send the user to the gateway, they pay, the bank redirects them back to your
   `CallbackURL`.
3. **Verify** — confirm the payment from the callback params.

## Quick start

```go
package main

import (
    "errors"
    "log"
    "net/http"
    "sync"

    "github.com/majid/payjet"
    "github.com/majid/payjet/zarinpal"
)

// Replace this in-memory store with your database.
var (
    mu    sync.Mutex
    store = map[string]*payjet.Payment{}
)

func main() {
    gw := zarinpal.New("xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx")

    http.HandleFunc("/checkout", func(w http.ResponseWriter, r *http.Request) {
        p := &payjet.Payment{
            Amount:      500_000, // Rials
            OrderID:     "order-123",
            CallbackURL: "https://myshop.ir/payment/callback",
            Description: "Order #123",
        }

        res, err := gw.Request(r.Context(), p)
        if err != nil {
            http.Error(w, err.Error(), http.StatusBadGateway)
            return
        }

        // Persist the payment, keyed by both OrderID and the gateway token, so it
        // can be found again in the callback regardless of which the gateway echoes.
        mu.Lock()
        store[p.OrderID] = p
        store[res.Token] = p
        mu.Unlock()

        // One call handles GET redirects and POST self-submitting forms.
        res.Redirect(w, r)
    })

    http.HandleFunc("/payment/callback", func(w http.ResponseWriter, r *http.Request) {
        params := payjet.ParseCallback(r)          // merges query + form fields
        key := gw.CallbackOrderID(params)          // gateway knows its own ID field

        mu.Lock()
        p, ok := store[key]
        mu.Unlock()
        if !ok {
            http.Error(w, "unknown payment", http.StatusBadRequest)
            return
        }

        result, err := gw.Verify(r.Context(), p, params)
        if err != nil {
            if errors.Is(err, payjet.ErrCancelled) {
                http.Redirect(w, r, "/payment/failed", http.StatusFound)
                return
            }
            log.Printf("verify error for %s: %v", p.OrderID, err)
            http.Redirect(w, r, "/payment/failed", http.StatusFound)
            return
        }

        log.Printf("paid: order=%s ref=%s card=%s", result.OrderID, result.RefID, result.CardNumber)
        http.Redirect(w, r, "/payment/success", http.StatusFound)
    })

    log.Fatal(http.ListenAndServe(":8080", nil))
}
```

A complete, runnable example using Gin lives in [`example/`](./example).

## Types

### Payment

```go
type Payment struct {
    Amount      int64  // in Rials, required
    OrderID     string // your unique order ID, required
    CallbackURL string // required
    Description string
    Mobile      string // optional
    Email       string // optional
}
```

Call `p.Validate()` to fail fast on missing required fields. `Request` already
calls it for you before making any network request.

### RequestResult

```go
type RequestResult struct {
    Token      string            // gateway token (authority, RefId, ...)
    PaymentURL string            // where to send the user
    Method     Method            // MethodGET or MethodPOST
    Params     map[string]string // form fields for POST gateways
}
```

Use `res.Redirect(w, r)` to send the user to the gateway. For GET gateways it
issues an HTTP 302; for POST gateways (Mellat, Saman) it writes a
self-submitting HTML form with all fields HTML-escaped.

### VerifyResult

```go
type VerifyResult struct {
    RefID      string            // gateway reference / trace number
    CardNumber string            // masked card number, when provided
    OrderID    string
    Amount     int64             // verified amount, when the gateway reports it
    RawParams  map[string]string // the callback params, for auditing
}
```

## Errors

`Verify` distinguishes a user cancelling from a real failure. Test with
`errors.Is`:

```go
result, err := gw.Verify(ctx, p, params)
switch {
case errors.Is(err, payjet.ErrCancelled):
    // user cancelled or the payment failed at the bank — let them retry
case errors.Is(err, payjet.ErrAmountMismatch):
    // verified amount differs from the requested amount
case errors.Is(err, payjet.ErrOrderMismatch):
    // callback order ID does not match the payment
case err != nil:
    // network / gateway fault
}
```

Gateway failures carry a structured `*payjet.Error` with the raw bank code:

```go
var ge *payjet.Error
if errors.As(err, &ge) {
    log.Printf("%s %s failed: code=%s msg=%s", ge.Gateway, ge.Op, ge.GatewayCode, ge.Message)
}
```

## Callback lookup

Each gateway echoes a different field in its callback, so `CallbackOrderID`
returns the right one for you. One gateway is special: **Zarinpal does not send
the merchant order ID back** — only its `Authority`, which equals
`RequestResult.Token`. Its `CallbackOrderID` therefore returns that token, so
store the payment under `res.Token` (as the quick start does) and the lookup
works uniformly across all gateways.

## Configuration options

Every gateway accepts functional options:

```go
zarinpal.New(id, zarinpal.WithSandbox())              // sandbox endpoints
idpay.New(key, idpay.WithSandbox())                   // X-SANDBOX header
mellat.New(cfg, mellat.WithEnglishPage())             // English payment page
gw := zarinpal.New(id, zarinpal.WithHTTPClient(c))    // custom *http.Client
gw := saman.New(id, saman.WithEndpoints(t, p, v))     // override URLs (testing)
```

Mellat and Pasargad take a config struct for their credentials:

```go
mellat.New(mellat.Config{
    TerminalID: 1234,
    Username:   "user",
    Password:   "pass",
})

pasargad.New(pasargad.Config{
    BaseURL:        "https://ipg.pasargadbank.ir/api/", // provided by the bank
    TerminalNumber: "12345678",
    Username:       "user",
    Password:       "pass",
})
```

When no `WithHTTPClient` is given, gateways use `payjet.DefaultHTTPClient()`,
which has a 30-second timeout.

## Testing without a bank

The `virtual` gateway implements `payjet.Gateway` and serves a local HTML
payment page, so you can develop and test the full flow with no real bank. It
also exposes `SimulatePayment` for browser-free unit tests:

```go
gw := virtual.New("http://localhost:8080/pay")

res, _ := gw.Request(ctx, payment)
params := gw.SimulatePayment(payment.OrderID, true) // true = pay, false = cancel
result, _ := gw.Verify(ctx, payment, params)
```

## License

No license file is present in this repository yet. Add a `LICENSE` before
publishing.
