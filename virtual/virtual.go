// Package virtual provides a test-only gateway that serves a local HTML payment
// page instead of connecting to a real bank. It implements payjet.Gateway so it
// can be swapped in for any real gateway during development and testing.
//
// Two testing modes:
//
//  1. Browser / integration testing — mount Handler() in your HTTP server, run
//     the app, click "Pay" or "Cancel" on the page.
//
//  2. Automated unit testing — call SimulatePayment() to get callback params
//     directly, no HTTP required.
package virtual

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/majid/payjet"
)

// Gateway is the virtual payment gateway.
type Gateway struct {
	gatewayURL string
	mu         sync.RWMutex
	pending    map[string]pendingPayment // token → payment
}

type pendingPayment struct {
	token       string
	orderID     string
	amount      int64
	callbackURL string
}

// Option configures a Gateway.
type Option func(*Gateway)

// New creates a virtual gateway. gatewayURL is the full public URL where
// Handler() is mounted (e.g. "http://localhost:8080/virtual-pay").
// It is used to build the PaymentURL returned by Request().
func New(gatewayURL string, opts ...Option) *Gateway {
	g := &Gateway{
		gatewayURL: strings.TrimRight(gatewayURL, "/"),
		pending:    make(map[string]pendingPayment),
	}
	for _, o := range opts {
		o(g)
	}
	return g
}

// ---- payjet.Gateway ---------------------------------------------------------

// Request stores the payment details and returns a redirect to the local HTML
// payment page.
func (g *Gateway) Request(ctx context.Context, p *payjet.Payment) (*payjet.RequestResult, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	token := newRandHex(16)
	g.mu.Lock()
	g.pending[token] = pendingPayment{
		token:       token,
		orderID:     p.OrderID,
		amount:      p.Amount,
		callbackURL: p.CallbackURL,
	}
	g.mu.Unlock()

	return &payjet.RequestResult{
		Token:      token,
		PaymentURL: g.gatewayURL + "?token=" + token,
		Method:     payjet.MethodGET,
	}, nil
}

// CallbackOrderID returns the OrderID echoed in the callback params.
func (g *Gateway) CallbackOrderID(params map[string]string) string {
	return params["OrderID"]
}

// Verify checks the callback params produced by the HTML handler or
// SimulatePayment. params must contain:
//
//	"result"          "true" or "false"
//	"OrderID"         the original order ID
//	"TransactionCode" random reference (present only when result=true)
func (g *Gateway) Verify(_ context.Context, p *payjet.Payment, params map[string]string) (*payjet.VerifyResult, error) {
	if params["result"] != "true" {
		return nil, payjet.Declined("virtual", "verify", params["result"], "")
	}
	txCode := params["TransactionCode"]
	if txCode == "" {
		txCode = newRandHex(12)
	}
	var amount int64
	if p != nil {
		amount = p.Amount
	}
	return &payjet.VerifyResult{
		RefID:     txCode,
		OrderID:   params["OrderID"],
		Amount:    amount,
		RawParams: params,
	}, nil
}

// ---- test helpers -----------------------------------------------------------

// SimulatePayment returns callback params as if the user clicked Pay (succeed=true)
// or Cancel (succeed=false) on the HTML page. Use this in go test to skip the
// browser step entirely:
//
//	gw := virtual.New("http://localhost:8080/pay")
//	req, _ := gw.Request(payment)
//	params := gw.SimulatePayment(payment.OrderID, true)
//	result, _ := gw.Verify(payment, params)
func (g *Gateway) SimulatePayment(orderID string, succeed bool) map[string]string {
	params := map[string]string{
		"OrderID": orderID,
		"result":  "false",
	}
	if succeed {
		params["result"] = "true"
		params["TransactionCode"] = newRandHex(12)
	}
	return params
}

// ---- HTTP handler -----------------------------------------------------------

// Handler returns an http.Handler that serves the HTML payment page.
// Mount it at the same path used in the gatewayURL passed to New:
//
//	gw := virtual.New("http://localhost:8080/virtual-pay")
//	mux.Handle("/virtual-pay", gw.Handler())
func (g *Gateway) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// token can come from query string (GET) or form body (POST)
		token := r.URL.Query().Get("token")
		if token == "" {
			token = r.FormValue("token")
		}

		g.mu.RLock()
		p, ok := g.pending[token]
		g.mu.RUnlock()

		if !ok {
			http.Error(w, "virtual gateway: unknown token", http.StatusBadRequest)
			return
		}

		if r.Method == http.MethodPost {
			isPaid := r.FormValue("pay") == "1"

			g.mu.Lock()
			delete(g.pending, token)
			g.mu.Unlock()

			q := url.Values{}
			q.Set("OrderID", p.orderID)
			if isPaid {
				q.Set("result", "true")
				q.Set("TransactionCode", newRandHex(12))
			} else {
				q.Set("result", "false")
			}

			target := p.callbackURL
			if strings.Contains(target, "?") {
				target += "&" + q.Encode()
			} else {
				target += "?" + q.Encode()
			}
			http.Redirect(w, r, target, http.StatusFound)
			return
		}

		// GET — render the payment page
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = pageTmpl.Execute(w, pageData{
			Token:  token,
			Order:  p.orderID,
			Amount: p.amount,
		})
	})
}

// ---- HTML template ----------------------------------------------------------

type pageData struct {
	Token  string
	Order  string
	Amount int64
}

var pageTmpl = template.Must(template.New("virtual").Parse(`<!DOCTYPE html>
<html lang="fa" dir="rtl">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>درگاه پرداخت مجازی</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:Tahoma,Arial,sans-serif;background:#f0f2f5;display:flex;align-items:center;justify-content:center;min-height:100vh}
.card{background:#fff;border-radius:12px;padding:2.5rem 2rem;box-shadow:0 4px 20px rgba(0,0,0,.1);width:360px;text-align:center}
.env-tag{display:inline-block;background:#fff3cd;color:#856404;font-size:.75rem;padding:.25rem .75rem;border-radius:20px;margin-bottom:1.5rem;border:1px solid #ffc107}
h1{font-size:1.1rem;color:#333;margin-bottom:1.5rem}
.row{margin-bottom:1rem}
.row .label{font-size:.75rem;color:#888;margin-bottom:.3rem}
.row .value{font-size:1rem;font-weight:600;color:#333;background:#f8f9fa;padding:.4rem .8rem;border-radius:6px;display:inline-block}
.actions{display:flex;gap:.75rem;margin-top:2rem;justify-content:center}
.btn{flex:1;padding:.75rem;border:none;border-radius:8px;font-size:.95rem;cursor:pointer;font-family:inherit;font-weight:600;transition:opacity .15s}
.btn:hover{opacity:.88}
.btn-pay{background:#28a745;color:#fff}
.btn-cancel{background:#dc3545;color:#fff}
.note{font-size:.7rem;color:#999;margin-top:1.5rem}
</style>
</head>
<body>
<div class="card">
  <div class="env-tag">⚠ محیط تست — درگاه مجازی</div>
  <h1>پرداخت اینترنتی</h1>
  <div class="row">
    <div class="label">شماره سفارش</div>
    <div class="value">{{.Order}}</div>
  </div>
  <div class="row">
    <div class="label">مبلغ (ریال)</div>
    <div class="value">{{.Amount}}</div>
  </div>
  <form method="POST">
    <input type="hidden" name="token" value="{{.Token}}">
    <div class="actions">
      <button class="btn btn-pay"    name="pay" value="1">پرداخت موفق ✓</button>
      <button class="btn btn-cancel" name="pay" value="0">انصراف ✗</button>
    </div>
  </form>
  <p class="note">این درگاه صرفاً برای توسعه و تست است و هیچ پرداخت واقعی انجام نمی‌دهد.</p>
</div>
</body>
</html>`))

// ---- utils ------------------------------------------------------------------

func newRandHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
