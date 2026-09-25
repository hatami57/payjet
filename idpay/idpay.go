package idpay

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/majid/payjet"
	"github.com/majid/payjet/internal/flexjson"
)

const (
	defaultRequestURL = "https://api.idpay.ir/v1.1/payment"
	defaultVerifyURL  = "https://api.idpay.ir/v1.1/payment/verify"

	// callback status "10" = ready for verification
	callbackReadyStatus = "10"
	// verify response status 100 = confirmed, 101 = confirmed before
	verifySuccessStatus         = "100"
	verifyAlreadyVerifiedStatus = "101"
)

type Gateway struct {
	apiKey     string
	requestURL string
	verifyURL  string
	sandbox    bool
	client     *http.Client
}

type Option func(*Gateway)

// WithSandbox adds the X-SANDBOX: 1 header to all requests.
func WithSandbox() Option {
	return func(g *Gateway) { g.sandbox = true }
}

// WithHTTPClient replaces the default HTTP client.
func WithHTTPClient(c *http.Client) Option {
	return func(g *Gateway) { g.client = c }
}

// WithEndpoints overrides the request and verify URLs.
func WithEndpoints(requestURL, verifyURL string) Option {
	return func(g *Gateway) {
		g.requestURL = requestURL
		g.verifyURL = verifyURL
	}
}

func New(apiKey string, opts ...Option) *Gateway {
	g := &Gateway{
		apiKey:     apiKey,
		requestURL: defaultRequestURL,
		verifyURL:  defaultVerifyURL,
		client:     payjet.DefaultHTTPClient(),
	}
	for _, o := range opts {
		o(g)
	}
	return g
}

// ---- helpers ----------------------------------------------------------------

func (g *Gateway) do(ctx context.Context, method, url string, body, out interface{}) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-KEY", g.apiKey)
	if g.sandbox {
		req.Header.Set("X-SANDBOX", "1")
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

// ---- request / verify -------------------------------------------------------

type requestBody struct {
	OrderID  string `json:"order_id"`
	Amount   int64  `json:"amount"`
	Phone    string `json:"phone,omitempty"`
	Mail     string `json:"mail,omitempty"`
	Desc     string `json:"desc,omitempty"`
	Callback string `json:"callback"`
}

type requestResponse struct {
	ID           string `json:"id"`
	Link         string `json:"link"`
	ErrorCode    int    `json:"error_code"`
	ErrorMessage string `json:"error_message"`
}

type verifyBody struct {
	ID      string `json:"id"`
	OrderID string `json:"order_id"`
}

type verifyResponse struct {
	Status  flexjson.String `json:"status"`
	TrackID flexjson.String `json:"track_id"`
	Amount  flexjson.String `json:"amount"`
	Payment struct {
		Amount flexjson.String `json:"amount"`
		CardNo string          `json:"card_no"`
	} `json:"payment"`
	ErrorCode    int    `json:"error_code"`
	ErrorMessage string `json:"error_message"`
}

// verifiedAmount is the amount IDPay reports for the verified payment, or "" if
// the response carries none.
func (r *verifyResponse) verifiedAmount() string {
	if r.Amount != "" {
		return string(r.Amount)
	}
	return string(r.Payment.Amount)
}

// CallbackOrderID returns the order_id IDPay echoes in the callback.
func (g *Gateway) CallbackOrderID(params map[string]string) string {
	return payjet.Param(params, "order_id")
}

func (g *Gateway) Request(ctx context.Context, p *payjet.Payment) (*payjet.RequestResult, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	var result requestResponse
	if err := g.do(ctx, http.MethodPost, g.requestURL, requestBody{
		OrderID:  p.OrderID,
		Amount:   p.Amount,
		Phone:    p.Mobile,
		Mail:     p.Email,
		Desc:     p.Description,
		Callback: p.CallbackURL,
	}, &result); err != nil {
		return nil, payjet.Fault("idpay", "request", "gateway call failed", err)
	}
	if result.ID == "" {
		return nil, payjet.Rejected("idpay", "request",
			strconv.Itoa(result.ErrorCode), result.ErrorMessage)
	}
	return &payjet.RequestResult{
		Token:      result.ID,
		PaymentURL: result.Link,
		Method:     payjet.MethodGET,
	}, nil
}

// Verify confirms the payment. The callback's order_id and amount must match p,
// and when p.Token is set it must match the callback's id.
func (g *Gateway) Verify(ctx context.Context, p *payjet.Payment, params map[string]string) (*payjet.VerifyResult, error) {
	status := payjet.Param(params, "status")
	if status != callbackReadyStatus {
		return nil, payjet.Declined("idpay", "verify", status, "payment not ready for verify")
	}
	id := payjet.Param(params, "id")
	if id == "" {
		return nil, payjet.Fault("idpay", "verify", "no id in callback", nil)
	}
	if p.Token != "" && id != p.Token {
		return nil, payjet.Mismatch("idpay", "verify", payjet.ErrTokenMismatch)
	}
	if payjet.Param(params, "order_id") != p.OrderID {
		return nil, payjet.Mismatch("idpay", "verify", payjet.ErrOrderMismatch)
	}
	if amount := payjet.Param(params, "amount"); amount != "" && amount != strconv.FormatInt(p.Amount, 10) {
		return nil, payjet.Mismatch("idpay", "verify", payjet.ErrAmountMismatch)
	}
	var result verifyResponse
	if err := g.do(ctx, http.MethodPost, g.verifyURL, verifyBody{
		ID:      id,
		OrderID: p.OrderID,
	}, &result); err != nil {
		return nil, payjet.Fault("idpay", "verify", "gateway call failed", err)
	}
	switch result.Status {
	case verifySuccessStatus, verifyAlreadyVerifiedStatus:
	case "":
		// Failures arrive as {"error_code", "error_message"} with no status.
		return nil, payjet.Rejected("idpay", "verify",
			strconv.Itoa(result.ErrorCode), result.ErrorMessage)
	default:
		return nil, payjet.Rejected("idpay", "verify", string(result.Status), result.ErrorMessage)
	}
	if amount := result.verifiedAmount(); amount != "" && amount != strconv.FormatInt(p.Amount, 10) {
		return nil, payjet.Mismatch("idpay", "verify", payjet.ErrAmountMismatch)
	}
	return &payjet.VerifyResult{
		RefID:           string(result.TrackID),
		CardNumber:      result.Payment.CardNo,
		OrderID:         p.OrderID,
		Amount:          p.Amount,
		RawParams:       params,
		AlreadyVerified: result.Status == verifyAlreadyVerifiedStatus,
	}, nil
}
