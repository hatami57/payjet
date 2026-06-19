// Package payjet is a multi-gateway payment SDK for Iranian bank gateways.
// All gateways implement the Gateway interface so applications can swap between
// them without changing call sites. All amounts are in Rials.
package payjet

import (
	"context"
	"html/template"
	"net/http"
	"time"

	"github.com/hatami57/microjet/core/errorx"
)

// DefaultTimeout is the timeout applied to the HTTP client each gateway uses by
// default. Override it per gateway with the package's WithHTTPClient option.
const DefaultTimeout = 30 * time.Second

// DefaultHTTPClient returns a new *http.Client with DefaultTimeout. Gateways use
// it when no custom client is supplied, so a hung bank endpoint cannot block a
// goroutine forever.
func DefaultHTTPClient() *http.Client {
	return &http.Client{Timeout: DefaultTimeout}
}

// Method is the HTTP method the user's browser must use to reach the payment page.
type Method string

const (
	MethodGET  Method = "GET"
	MethodPOST Method = "POST"
)

// Gateway is the common interface all payment gateways implement.
type Gateway interface {
	// Request initiates a payment and returns redirect info.
	Request(ctx context.Context, p *Payment) (*RequestResult, error)
	// Verify confirms the payment after the user returns from the gateway.
	// params contains the raw POST/GET fields sent to the callback URL
	// (use ParseCallback to build it from an *http.Request).
	Verify(ctx context.Context, p *Payment, params map[string]string) (*VerifyResult, error)
	// CallbackOrderID returns the value identifying which pending payment a set
	// of callback params belongs to. For most gateways this is the merchant
	// OrderID echoed in the callback; for gateways that do not echo it (Zarinpal)
	// it is the gateway token, which equals RequestResult.Token — store the
	// payment under that token so the lookup still succeeds.
	CallbackOrderID(params map[string]string) string
}

// Payment holds the details for a payment transaction.
type Payment struct {
	Amount      int64  // in Rials
	OrderID     string // merchant-assigned unique order ID
	CallbackURL string
	Description string
	Mobile      string // optional
	Email       string // optional
}

// Validate reports whether the payment has the fields every gateway requires.
// Call it before Request to fail fast instead of after a wasted round-trip.
func (p *Payment) Validate() error {
	switch {
	case p == nil:
		return errorx.NewBadRequestError("payment", "payment is nil")
	case p.Amount <= 0:
		return errorx.NewBadRequestError("payment", "Amount must be positive").
			WithParams("amount", p.Amount)
	case p.OrderID == "":
		return errorx.NewBadRequestError("payment", "OrderID is required")
	case p.CallbackURL == "":
		return errorx.NewBadRequestError("payment", "CallbackURL is required")
	}
	return nil
}

// RequestResult is returned by Gateway.Request.
type RequestResult struct {
	// Token is the gateway-issued token (authority, RefId, etc.)
	Token string
	// PaymentURL is where the user should be sent.
	PaymentURL string
	// Method is MethodGET or MethodPOST. When MethodPOST, Params holds the form fields.
	Method Method
	// Params contains form fields for POST-based gateways (Mellat, Saman).
	Params map[string]string
}

// Redirect sends the user to the gateway's payment page. For GET gateways it
// issues an HTTP 302 redirect; for POST gateways it writes a self-submitting
// HTML form (fields are HTML-escaped). Call it from your checkout handler:
//
//	res, err := gw.Request(ctx, p)
//	if err != nil { ... }
//	res.Redirect(w, r)
func (r *RequestResult) Redirect(w http.ResponseWriter, req *http.Request) error {
	if r.Method == MethodPOST {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		return autoPostTmpl.Execute(w, r)
	}
	http.Redirect(w, req, r.PaymentURL, http.StatusFound)
	return nil
}

var autoPostTmpl = template.Must(template.New("redirect").Parse(`<!DOCTYPE html>
<html><head><meta charset="UTF-8"><title>در حال انتقال...</title></head>
<body onload="document.forms[0].submit()">
<form method="POST" action="{{.PaymentURL}}">
{{range $k, $v := .Params}}<input type="hidden" name="{{$k}}" value="{{$v}}">
{{end}}<noscript><button type="submit">ادامه</button></noscript>
</form></body></html>`))

// VerifyResult is returned by a successful Gateway.Verify.
type VerifyResult struct {
	RefID      string // gateway reference / trace number
	CardNumber string // masked card number, if provided
	OrderID    string
	Amount     int64             // verified amount in Rials, when the gateway reports it
	RawParams  map[string]string // the callback params, for auditing/reconciliation
}

// ParseCallback collects the callback fields from an HTTP request, merging URL
// query parameters and POST form fields into a single map. It works for both
// GET and POST callbacks regardless of which the gateway uses.
func ParseCallback(r *http.Request) map[string]string {
	params := make(map[string]string)
	for k, v := range r.URL.Query() {
		if len(v) > 0 {
			params[k] = v[0]
		}
	}
	if err := r.ParseForm(); err == nil {
		for k, v := range r.PostForm {
			if len(v) > 0 {
				params[k] = v[0]
			}
		}
	}
	return params
}
