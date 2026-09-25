package pasargad

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/majid/payjet"
)

// Pasargad uses a merchant-specific base URL provided by the bank.
// Paths below are appended to that base URL.
const (
	defaultGetTokenPath = "Token/GetToken"
	defaultPurchasePath = "Api/Payment/purchase"
	defaultVerifyPath   = "Api/Payment/Verify-Payment"
	defaultReversePath  = "Api/Payment/Reverse-Transactions"
)

var _ payjet.Refunder = (*Gateway)(nil)

type Gateway struct {
	baseURL        string
	terminalNumber string
	username       string
	password       string
	getTokenPath   string
	purchasePath   string
	verifyPath     string
	reversePath    string
	client         *http.Client
}

type Option func(*Gateway)

// WithHTTPClient replaces the default HTTP client.
func WithHTTPClient(c *http.Client) Option {
	return func(g *Gateway) { g.client = c }
}

// WithPaths overrides the API path segments relative to the base URL.
// Pass an empty string to keep the current value.
func WithPaths(getTokenPath, purchasePath, verifyPath string) Option {
	return func(g *Gateway) {
		if getTokenPath != "" {
			g.getTokenPath = getTokenPath
		}
		if purchasePath != "" {
			g.purchasePath = purchasePath
		}
		if verifyPath != "" {
			g.verifyPath = verifyPath
		}
	}
}

// WithReversePath overrides the reverse (refund) API path relative to the base URL.
func WithReversePath(reversePath string) Option {
	return func(g *Gateway) { g.reversePath = reversePath }
}

// Config holds the merchant settings for a Pasargad terminal. BaseURL is
// merchant-specific and provided by the bank (e.g. "https://ipg.pasargadbank.ir/api/").
type Config struct {
	BaseURL        string
	TerminalNumber string
	Username       string
	Password       string
}

// New creates a Pasargad gateway from the given config.
func New(cfg Config, opts ...Option) *Gateway {
	g := &Gateway{
		baseURL:        strings.TrimRight(cfg.BaseURL, "/") + "/",
		terminalNumber: cfg.TerminalNumber,
		username:       cfg.Username,
		password:       cfg.Password,
		getTokenPath:   defaultGetTokenPath,
		purchasePath:   defaultPurchasePath,
		verifyPath:     defaultVerifyPath,
		reversePath:    defaultReversePath,
		client:         payjet.DefaultHTTPClient(),
	}
	for _, o := range opts {
		o(g)
	}
	return g
}

// ---- HTTP helper ------------------------------------------------------------

func (g *Gateway) post(ctx context.Context, path, bearerToken string, body, out interface{}) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

// ---- auth -------------------------------------------------------------------

type tokenRequest struct {
	Username string `json:"Username"`
	Password string `json:"Password"`
}

type tokenResponse struct {
	Token      string `json:"Token"`
	ResultCode int    `json:"ResultCode"`
	ResultMsg  string `json:"ResultMsg"`
}

func (g *Gateway) getToken(ctx context.Context) (string, error) {
	var result tokenResponse
	if err := g.post(ctx, g.getTokenPath, "", tokenRequest{Username: g.username, Password: g.password}, &result); err != nil {
		return "", payjet.Fault("pasargad", "auth", "gateway call failed", err)
	}
	if result.ResultCode != 0 || result.Token == "" {
		return "", payjet.Rejected("pasargad", "auth",
			strconv.Itoa(result.ResultCode), result.ResultMsg)
	}
	return result.Token, nil
}

// authFault reports a failed login during op. Once a payment exists, the bank
// refusing our credentials says nothing about the payment itself, so it is a
// fault to retry, not a rejection that would fail the order.
func authFault(op string, err error) error {
	return payjet.Fault("pasargad", op, "authentication failed", err)
}

// ---- request / verify -------------------------------------------------------

type purchaseRequest struct {
	TerminalNumber string  `json:"TerminalNumber"`
	Invoice        string  `json:"Invoice"`
	InvoiceDate    string  `json:"InvoiceDate"`
	Amount         float64 `json:"Amount"`
	CallbackApi    string  `json:"CallbackApi"`
	ServiceCode    int     `json:"ServiceCode"`
	ServiceType    string  `json:"ServiceType"`
	MobileNumber   string  `json:"MobileNumber,omitempty"`
	Description    string  `json:"Description,omitempty"`
	PayerMail      string  `json:"PayerMail,omitempty"`
}

type purchaseResponse struct {
	ResultCode int    `json:"ResultCode"`
	ResultMsg  string `json:"ResultMsg"`
	Data       struct {
		UrlId string `json:"UrlId"`
		Url   string `json:"Url"`
	} `json:"Data"`
}

type verifyRequest struct {
	Invoice string `json:"Invoice"`
	UrlId   string `json:"UrlId"`
}

type verifyResponse struct {
	ResultCode int    `json:"ResultCode"`
	ResultMsg  string `json:"ResultMsg"`
}

// CallbackOrderID returns the invoiceId (the merchant order ID) Pasargad echoes back.
func (g *Gateway) CallbackOrderID(params map[string]string) string {
	return payjet.Param(params, "invoiceId")
}

func (g *Gateway) Request(ctx context.Context, p *payjet.Payment) (*payjet.RequestResult, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	token, err := g.getToken(ctx)
	if err != nil {
		return nil, err
	}
	var result purchaseResponse
	if err := g.post(ctx, g.purchasePath, token, purchaseRequest{
		TerminalNumber: g.terminalNumber,
		Invoice:        p.OrderID,
		InvoiceDate:    time.Now().Format("2006/01/02 15:04:05"),
		Amount:         float64(p.Amount),
		CallbackApi:    p.CallbackURL,
		ServiceCode:    8,
		ServiceType:    "PURCHASE",
		MobileNumber:   p.Mobile,
		Description:    p.Description,
		PayerMail:      p.Email,
	}, &result); err != nil {
		return nil, payjet.Fault("pasargad", "request", "gateway call failed", err)
	}
	if result.ResultCode != 0 {
		return nil, payjet.Rejected("pasargad", "request",
			strconv.Itoa(result.ResultCode), result.ResultMsg)
	}
	if result.Data.UrlId == "" || result.Data.Url == "" {
		return nil, payjet.Fault("pasargad", "request", "purchase succeeded without a UrlId or payment URL", nil)
	}
	return &payjet.RequestResult{
		Token:      result.Data.UrlId,
		PaymentURL: result.Data.Url,
		Method:     payjet.MethodGET,
	}, nil
}

// Verify confirms the payment. p.Token must be the RequestResult.Token (the
// purchase UrlId) issued for p: Pasargad's callback carries only invoiceId,
// status, referenceNumber and trackId, and Verify-Payment needs the UrlId.
func (g *Gateway) Verify(ctx context.Context, p *payjet.Payment, params map[string]string) (*payjet.VerifyResult, error) {
	if p == nil {
		return nil, payjet.Invalid("pasargad", "verify", "payment is nil")
	}
	status := payjet.Param(params, "status")
	if !strings.EqualFold(status, "success") {
		return nil, payjet.Declined("pasargad", "verify", status, "")
	}
	if p.Token == "" {
		return nil, payjet.Invalid("pasargad", "verify", "Payment.Token (the purchase UrlId) is required to verify a Pasargad payment")
	}
	if payjet.Param(params, "invoiceId") != p.OrderID {
		return nil, payjet.Mismatch("pasargad", "verify", payjet.ErrOrderMismatch)
	}
	token, err := g.getToken(ctx)
	if err != nil {
		return nil, authFault("verify", err)
	}
	var result verifyResponse
	if err := g.post(ctx, g.verifyPath, token, verifyRequest{
		Invoice: p.OrderID,
		UrlId:   p.Token,
	}, &result); err != nil {
		return nil, payjet.Fault("pasargad", "verify", "gateway call failed", err)
	}
	if result.ResultCode != 0 {
		return nil, payjet.Rejected("pasargad", "verify",
			strconv.Itoa(result.ResultCode), result.ResultMsg)
	}
	return &payjet.VerifyResult{
		RefID:     payjet.Param(params, "referenceNumber"),
		OrderID:   p.OrderID,
		Amount:    p.Amount,
		RawParams: params,
	}, nil
}

type reverseRequest struct {
	Invoice string `json:"Invoice"`
	UrlId   string `json:"UrlId"`
}

// reverseResponse covers both shapes Pasargad's reverse API is known by:
// {IsSuccess, Message} (as Parbad reads it) and the {ResultCode, ResultMsg}
// its other endpoints return.
type reverseResponse struct {
	IsSuccess  bool   `json:"IsSuccess"`
	Message    string `json:"Message"`
	ResultCode *int   `json:"ResultCode"`
	ResultMsg  string `json:"ResultMsg"`
}

func (r *reverseResponse) failure() (code, message string, failed bool) {
	if r.IsSuccess || (r.ResultCode != nil && *r.ResultCode == 0) {
		return "", "", false
	}
	message = r.Message
	if message == "" {
		message = r.ResultMsg
	}
	if r.ResultCode != nil {
		code = strconv.Itoa(*r.ResultCode)
	}
	return code, message, true
}

// Refund reverses the whole payment. It needs p.Token, the purchase UrlId
// Request issued; v is not used.
func (g *Gateway) Refund(ctx context.Context, p *payjet.Payment, _ *payjet.VerifyResult) (*payjet.RefundResult, error) {
	if p == nil {
		return nil, payjet.Invalid("pasargad", "refund", "payment is nil")
	}
	if p.Token == "" {
		return nil, payjet.Invalid("pasargad", "refund", "Payment.Token (the purchase UrlId) is required to refund a Pasargad payment")
	}
	token, err := g.getToken(ctx)
	if err != nil {
		return nil, authFault("refund", err)
	}
	var result reverseResponse
	if err := g.post(ctx, g.reversePath, token, reverseRequest{
		Invoice: p.OrderID,
		UrlId:   p.Token,
	}, &result); err != nil {
		return nil, payjet.Fault("pasargad", "refund", "gateway call failed", err)
	}
	if code, message, failed := result.failure(); failed {
		return nil, payjet.Rejected("pasargad", "refund", code, message)
	}
	return &payjet.RefundResult{OrderID: p.OrderID, Amount: p.Amount, RefID: p.Token}, nil
}
