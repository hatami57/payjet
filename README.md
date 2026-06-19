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

Runnable examples live in [`examples/`](./examples), from a complete
PostgreSQL-backed web shop to focused, dependency-free demos. See
[Examples](#examples).

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

Amounts are plain `int64` Rials. If your application already speaks microjet's
currency-aware `money.Money`, bridge in and out with the helpers in
[Built on microjet](#built-on-microjet):

```go
m := p.Money()                       // money.Money tagged IRR
rials, err := payjet.ToRials(m)      // back to int64, validates currency + whole number
amount := payjet.RialMoney(500_000)  // build a money.Money from Rials
```

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

Gateway failures are structured [`*errorx.Error`](https://github.com/hatami57/microjet)
values (from microjet — see [Built on microjet](#built-on-microjet)), so they
carry a category, the gateway name (as the error `Subject`), and the raw bank
code and operation (in `Params`). The category maps straight to an HTTP status
through microjet's HTTP error middleware: declines and rejections are `Business`
(409), bad caller input is `BadRequest` (400), and transport/parse faults are
`Internal` (500). Inspect them with microjet's helpers:

```go
import "github.com/hatami57/microjet/core/errorx"

if ce := errorx.GetError(err); ce != nil {
    log.Printf("%s failed: code=%v msg=%s", ce.Subject, ce.Params["gatewayCode"], ce.Message)
}
switch {
case errorx.IsBusinessError(err):   // bank declined / rejected → 409
case errorx.IsBadRequestError(err): // invalid request → 400
case errorx.IsInternalError(err):   // gateway fault → 500
}
```

The sentinels above still work: they are carried as the error's `Inner`, so
`errors.Is(err, payjet.ErrCancelled)` and the category checks both hold on the
same error.

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

## Persistence

payjet keeps storage behind two interfaces so you can use any backend, and ships
gormx-backed defaults so you don't have to implement either when running on
microjet's database stack.

```go
type PaymentStore interface {
    SavePayment(ctx context.Context, p *StoredPayment) error
    GetPayment(ctx context.Context, orderID string) (*StoredPayment, error)
    GetPaymentByToken(ctx context.Context, token string) (*StoredPayment, error)
    SetStatus(ctx context.Context, orderID string, status PaymentStatus) error
}

type TransactionStore interface {
    SaveTransaction(ctx context.Context, t *Transaction) error
    GetTransaction(ctx context.Context, orderID string) (*Transaction, error)
    ListTransactions(ctx context.Context, orderID string) ([]*Transaction, error)
}
```

`StoredPayment` is the order/intent (keyed by `OrderID`, indexed by gateway
`Token` for token-only callbacks like Zarinpal); `Transaction` is the persisted
outcome of a `Verify` (ref/trace number, masked card, amount, and the raw
callback params for reconciliation). The helpers `payjet.NewStoredPayment` and
`payjet.NewTransaction` build them from a `Payment` and a `VerifyResult`.

### Default stores via the module

`payjet.Module()` is a microjet module that registers the gormx-backed
`PaymentStore` and `TransactionStore`. They share the host's `*gorm.DB`
(`app.DB()`), so payments and transactions land in the same database as the rest
of your app, and the module auto-migrates the `payjet_payments` and
`payjet_transactions` tables — no storage code and no migrations to write:

```go
app := host.MustNew().
    WithDatabase(postgres.Driver()).
    WithModule(payjet.Module()).
    MustRun()

// resolve the stores anywhere from the service container:
ps := host.MustResolveService[payjet.PaymentStore](app)
ts := host.MustResolveService[payjet.TransactionStore](app)
```

### Bringing your own storage

Implement either interface and substitute it without touching the rest:

```go
payjet.Module(
    payjet.WithPaymentStore(myRedisPaymentStore),
    payjet.WithTransactionStore(myMongoTxStore),
)
```

A custom store is just a service: if it implements microjet's `Init`/`Setup`
lifecycle it is wired the same way the defaults are.

## Examples

All examples live under [`examples/`](./examples). The focused ones need no
database, no bank, and no HTTP server — just `go run .` in the directory:

| Example | What it shows | Run |
| --- | --- | --- |
| [`virtual`](./examples/virtual) | The core `Gateway` flow end to end (Request → simulated payment → Verify) with the virtual gateway and the money helpers. | `go run ./examples/virtual` |
| [`errors`](./examples/errors) | Inspecting failures: the `errorx` category helpers, the structured payload, and the `errors.Is` sentinels. | `go run ./examples/errors` |
| [`storage`](./examples/storage) | Implementing `PaymentStore` and `TransactionStore` over your own backend (the "bring your own storage" path). | `go run ./examples/storage` |
| [`webshop`](./examples/webshop) | A complete checkout app on the microjet `host`: any gateway via config, the default gormx stores via `payjet.Module()`, callbacks, and a PostgreSQL database. | needs PostgreSQL — see its `config.toml` |

## Built on microjet

payjet builds on [microjet](https://github.com/hatami57/microjet) instead of
reinventing the same infrastructure:

- **Errors** — gateway failures are microjet `errorx.Error` values, so they carry
  a category that maps to an HTTP status and serialize to a structured JSON body
  through microjet's HTTP error middleware. See [Errors](#errors).
- **Money** — `payjet.RialMoney`, `payjet.ToRials`, and the `Money()` accessors
  bridge `int64` Rials to microjet's currency-aware `money.Money` (`CurrencyIRR`).
- **Persistence** — the `PaymentStore` and `TransactionStore` interfaces let you
  back payjet with any storage, while `payjet.Module()` wires gormx-backed
  defaults that persist into the host's database. See [Persistence](#persistence).
- **Examples** — [`examples/webshop`](./examples/webshop) runs on the microjet
  `host` orchestrator: TOML config (`config.toml`, gateway selected under
  `[payjet]`), the default payjet stores registered with `payjet.Module()`,
  structured `slog` logging, the built-in gin server with health/logging/recovery
  middleware, and graceful shutdown — the whole app is one
  `host.MustNew().Configure(cfg).WithDatabase(postgres.Driver()).WithModule(payjet.Module()).WithHTTPServer(...).MustRun()`
  chain. See [Examples](#examples) for the focused demos.

payjet pins microjet's modules at `v0.18.0` and uses `replace` directives in
`go.mod` to build against a sibling `../microjet` checkout during development.
To consume payjet via `go get` against published microjet modules, drop the
replaces.

## License

No license file is present in this repository yet. Add a `LICENSE` before
publishing.
