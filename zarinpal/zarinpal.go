package zarinpal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/majid/payjet"
)

const (
	defaultRequestURL = "https://api.zarinpal.com/pg/v4/payment/request.json"
	defaultVerifyURL  = "https://api.zarinpal.com/pg/v4/payment/verify.json"
	defaultPaymentURL = "https://www.zarinpal.com/pg/StartPay/"

	sandboxRequestURL = "https://sandbox.zarinpal.com/pg/v4/payment/request.json"
	sandboxVerifyURL  = "https://sandbox.zarinpal.com/pg/v4/payment/verify.json"
	sandboxPaymentURL = "https://sandbox.zarinpal.com/pg/StartPay/"
)

type Gateway struct {
	merchantID string
	requestURL string
	verifyURL  string
	paymentURL string
	client     *http.Client
}

type Option func(*Gateway)

// WithSandbox switches all endpoints to the Zarinpal sandbox environment.
func WithSandbox() Option {
	return func(g *Gateway) {
		g.requestURL = sandboxRequestURL
		g.verifyURL = sandboxVerifyURL
		g.paymentURL = sandboxPaymentURL
	}
}

// WithHTTPClient replaces the default HTTP client (e.g. to set timeouts or a proxy).
func WithHTTPClient(c *http.Client) Option {
	return func(g *Gateway) { g.client = c }
}

// WithEndpoints overrides the request, verify, and payment page URLs.
func WithEndpoints(requestURL, verifyURL, paymentURL string) Option {
	return func(g *Gateway) {
		g.requestURL = requestURL
		g.verifyURL = verifyURL
		g.paymentURL = paymentURL
	}
}

func New(merchantID string, opts ...Option) *Gateway {
	g := &Gateway{
		merchantID: merchantID,
		requestURL: defaultRequestURL,
		verifyURL:  defaultVerifyURL,
		paymentURL: defaultPaymentURL,
		client:     payjet.DefaultHTTPClient(),
	}
	for _, o := range opts {
		o(g)
	}
	return g
}

// ---- request / verify -------------------------------------------------------

type requestBody struct {
	MerchantID  string `json:"merchant_id"`
	Amount      int64  `json:"amount"`
	CallbackURL string `json:"callback_url"`
	Description string `json:"description"`
	Mobile      string `json:"mobile,omitempty"`
	Email       string `json:"email,omitempty"`
}

type requestData struct {
	Code      int    `json:"code"`
	Message   string `json:"message"`
	Authority string `json:"authority"`
}

type verifyBody struct {
	MerchantID string `json:"merchant_id"`
	Amount     int64  `json:"amount"`
	Authority  string `json:"authority"`
}

type verifyData struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	RefID   int64  `json:"ref_id"`
	CardPan string `json:"card_pan"`
}

// codePaymentFailed is Zarinpal's error for a payment that did not complete.
const codePaymentFailed = -51

// apiError is the error Zarinpal reports under "errors", with "data" empty.
type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// parseAPIError reads the "errors" member, which Zarinpal sends as an object and
// some endpoints as an array; an empty array or null means no error.
func parseAPIError(raw json.RawMessage) *apiError {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	var e apiError
	switch raw[0] {
	case '{':
		if json.Unmarshal(raw, &e) != nil {
			return nil
		}
	case '[':
		var list []apiError
		if json.Unmarshal(raw, &list) != nil || len(list) == 0 {
			return nil
		}
		e = list[0]
	default:
		return nil
	}
	if e.Code == 0 {
		return nil
	}
	return &e
}

// postJSON posts body and decodes the response's "data" member into data. A
// failure Zarinpal reports under "errors" is returned as an *apiError, not an
// error, so callers can map its code; err is for transport and decoding faults.
func (g *Gateway) postJSON(ctx context.Context, url string, body, data interface{}) (*apiError, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decoding HTTP %d response: %w", resp.StatusCode, err)
	}
	if e := parseAPIError(env.Errors); e != nil {
		return e, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected HTTP %d with no error in the response", resp.StatusCode)
	}
	if err := json.Unmarshal(env.Data, data); err != nil {
		return nil, fmt.Errorf("decoding response data: %w", err)
	}
	return nil, nil
}

// CallbackOrderID returns the Authority echoed in the callback. Zarinpal does not
// send the merchant order ID back, so store the payment under RequestResult.Token
// (which equals the Authority) to look it up here.
func (g *Gateway) CallbackOrderID(params map[string]string) string {
	return payjet.Param(params, "Authority")
}

func (g *Gateway) Request(ctx context.Context, p *payjet.Payment) (*payjet.RequestResult, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	var result requestData
	apiErr, err := g.postJSON(ctx, g.requestURL, requestBody{
		MerchantID:  g.merchantID,
		Amount:      p.Amount,
		CallbackURL: p.CallbackURL,
		Description: p.Description,
		Mobile:      p.Mobile,
		Email:       p.Email,
	}, &result)
	if err != nil {
		return nil, payjet.Fault("zarinpal", "request", "gateway call failed", err)
	}
	if apiErr != nil {
		return nil, payjet.Rejected("zarinpal", "request", strconv.Itoa(apiErr.Code), apiErr.Message)
	}
	if result.Code != 100 || result.Authority == "" {
		return nil, payjet.Rejected("zarinpal", "request", strconv.Itoa(result.Code), result.Message)
	}
	return &payjet.RequestResult{
		Token:      result.Authority,
		PaymentURL: g.paymentURL + result.Authority,
		Method:     payjet.MethodGET,
	}, nil
}

// Verify confirms the payment. When p.Token is set it must match the callback's
// Authority.
func (g *Gateway) Verify(ctx context.Context, p *payjet.Payment, params map[string]string) (*payjet.VerifyResult, error) {
	status := payjet.Param(params, "Status")
	if !strings.EqualFold(status, "OK") {
		return nil, payjet.Declined("zarinpal", "verify", status, "")
	}
	authority := payjet.Param(params, "Authority")
	if authority == "" {
		return nil, payjet.Fault("zarinpal", "verify", "no Authority in callback", nil)
	}
	if p.Token != "" && authority != p.Token {
		return nil, payjet.Mismatch("zarinpal", "verify", payjet.ErrTokenMismatch)
	}
	var result verifyData
	apiErr, err := g.postJSON(ctx, g.verifyURL, verifyBody{
		MerchantID: g.merchantID,
		Amount:     p.Amount,
		Authority:  authority,
	}, &result)
	if err != nil {
		return nil, payjet.Fault("zarinpal", "verify", "gateway call failed", err)
	}
	if apiErr != nil {
		code := strconv.Itoa(apiErr.Code)
		if apiErr.Code == codePaymentFailed {
			return nil, payjet.Declined("zarinpal", "verify", code, apiErr.Message)
		}
		return nil, payjet.Rejected("zarinpal", "verify", code, apiErr.Message)
	}
	// 101 = already verified: the payment is genuine, but this is a repeat.
	if result.Code != 100 && result.Code != 101 {
		return nil, payjet.Rejected("zarinpal", "verify", strconv.Itoa(result.Code), result.Message)
	}
	return &payjet.VerifyResult{
		RefID:           strconv.FormatInt(result.RefID, 10),
		CardNumber:      result.CardPan,
		OrderID:         p.OrderID,
		Amount:          p.Amount,
		RawParams:       params,
		AlreadyVerified: result.Code == 101,
	}, nil
}
