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
)

type Gateway struct {
	baseURL        string
	terminalNumber string
	username       string
	password       string
	getTokenPath   string
	purchasePath   string
	verifyPath     string
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
		return "", err
	}
	if result.ResultCode != 0 || result.Token == "" {
		return "", &payjet.Error{Gateway: "pasargad", Op: "auth",
			GatewayCode: strconv.Itoa(result.ResultCode), Message: result.ResultMsg}
	}
	return result.Token, nil
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
	return params["invoiceId"]
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
		return nil, err
	}
	if result.ResultCode != 0 {
		return nil, &payjet.Error{Gateway: "pasargad", Op: "request",
			GatewayCode: strconv.Itoa(result.ResultCode), Message: result.ResultMsg}
	}
	return &payjet.RequestResult{
		Token:      result.Data.UrlId,
		PaymentURL: result.Data.Url,
		Method:     payjet.MethodGET,
	}, nil
}

func (g *Gateway) Verify(ctx context.Context, p *payjet.Payment, params map[string]string) (*payjet.VerifyResult, error) {
	if strings.ToLower(params["status"]) != "success" {
		return nil, &payjet.Error{Gateway: "pasargad", Op: "verify",
			GatewayCode: params["status"], Err: payjet.ErrCancelled}
	}
	if params["invoiceId"] != p.OrderID {
		return nil, &payjet.Error{Gateway: "pasargad", Op: "verify", Err: payjet.ErrOrderMismatch}
	}
	token, err := g.getToken(ctx)
	if err != nil {
		return nil, err
	}
	var result verifyResponse
	if err := g.post(ctx, g.verifyPath, token, verifyRequest{
		Invoice: p.OrderID,
		UrlId:   params["urlId"],
	}, &result); err != nil {
		return nil, err
	}
	if result.ResultCode != 0 {
		return nil, &payjet.Error{Gateway: "pasargad", Op: "verify",
			GatewayCode: strconv.Itoa(result.ResultCode), Message: result.ResultMsg}
	}
	return &payjet.VerifyResult{
		RefID:     params["referenceNumber"],
		OrderID:   p.OrderID,
		Amount:    p.Amount,
		RawParams: params,
	}, nil
}
