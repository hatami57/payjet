package idpay

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/majid/payjet"
)

const (
	defaultRequestURL = "https://api.idpay.ir/v1.1/payment"
	defaultVerifyURL  = "https://api.idpay.ir/v1.1/payment/verify"

	// callback status "10" = ready for verification
	callbackReadyStatus = "10"
	// verify response status 100 = confirmed
	verifySuccessStatus = 100
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
	Status  int   `json:"status"`
	TrackID int64 `json:"track_id"`
	Payment struct {
		CardNo string `json:"card_no"`
	} `json:"payment"`
	ErrorCode    int    `json:"error_code"`
	ErrorMessage string `json:"error_message"`
}

// CallbackOrderID returns the order_id IDPay echoes in the callback.
func (g *Gateway) CallbackOrderID(params map[string]string) string {
	return params["order_id"]
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
		return nil, err
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

func (g *Gateway) Verify(ctx context.Context, p *payjet.Payment, params map[string]string) (*payjet.VerifyResult, error) {
	if params["status"] != callbackReadyStatus {
		return nil, payjet.Declined("idpay", "verify",
			params["status"], "payment not ready for verify")
	}
	var result verifyResponse
	if err := g.do(ctx, http.MethodPost, g.verifyURL, verifyBody{
		ID:      params["id"],
		OrderID: params["order_id"],
	}, &result); err != nil {
		return nil, err
	}
	if result.Status != verifySuccessStatus {
		return nil, payjet.Rejected("idpay", "verify",
			strconv.Itoa(result.Status), result.ErrorMessage)
	}
	return &payjet.VerifyResult{
		RefID:      strconv.FormatInt(result.TrackID, 10),
		CardNumber: result.Payment.CardNo,
		OrderID:    params["order_id"],
		Amount:     p.Amount,
		RawParams:  params,
	}, nil
}
