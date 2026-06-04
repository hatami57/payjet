package zarinpal

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"

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

type requestResponse struct {
	Data struct {
		Code      int    `json:"code"`
		Message   string `json:"message"`
		Authority string `json:"authority"`
	} `json:"data"`
}

type verifyBody struct {
	MerchantID string `json:"merchant_id"`
	Amount     int64  `json:"amount"`
	Authority  string `json:"authority"`
}

type verifyResponse struct {
	Data struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		RefID   int64  `json:"ref_id"`
		CardPan string `json:"card_pan"`
	} `json:"data"`
}

func (g *Gateway) postJSON(ctx context.Context, url string, body, out interface{}) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

// CallbackOrderID returns the Authority echoed in the callback. Zarinpal does not
// send the merchant order ID back, so store the payment under RequestResult.Token
// (which equals the Authority) to look it up here.
func (g *Gateway) CallbackOrderID(params map[string]string) string {
	return params["Authority"]
}

func (g *Gateway) Request(ctx context.Context, p *payjet.Payment) (*payjet.RequestResult, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	var result requestResponse
	if err := g.postJSON(ctx, g.requestURL, requestBody{
		MerchantID:  g.merchantID,
		Amount:      p.Amount,
		CallbackURL: p.CallbackURL,
		Description: p.Description,
		Mobile:      p.Mobile,
		Email:       p.Email,
	}, &result); err != nil {
		return nil, err
	}
	if result.Data.Code != 100 {
		return nil, &payjet.Error{Gateway: "zarinpal", Op: "request",
			GatewayCode: strconv.Itoa(result.Data.Code), Message: result.Data.Message}
	}
	return &payjet.RequestResult{
		Token:      result.Data.Authority,
		PaymentURL: g.paymentURL + result.Data.Authority,
		Method:     payjet.MethodGET,
	}, nil
}

func (g *Gateway) Verify(ctx context.Context, p *payjet.Payment, params map[string]string) (*payjet.VerifyResult, error) {
	if params["Status"] != "OK" {
		return nil, &payjet.Error{Gateway: "zarinpal", Op: "verify", Err: payjet.ErrCancelled}
	}
	var result verifyResponse
	if err := g.postJSON(ctx, g.verifyURL, verifyBody{
		MerchantID: g.merchantID,
		Amount:     p.Amount,
		Authority:  params["Authority"],
	}, &result); err != nil {
		return nil, err
	}
	// 101 = already verified — idempotent success
	if result.Data.Code != 100 && result.Data.Code != 101 {
		return nil, &payjet.Error{Gateway: "zarinpal", Op: "verify",
			GatewayCode: strconv.Itoa(result.Data.Code), Message: result.Data.Message}
	}
	return &payjet.VerifyResult{
		RefID:      strconv.FormatInt(result.Data.RefID, 10),
		CardNumber: result.Data.CardPan,
		OrderID:    p.OrderID,
		Amount:     p.Amount,
		RawParams:  params,
	}, nil
}
