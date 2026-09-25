package saman

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/majid/payjet"
)

const (
	defaultTokenURL   = "https://sep.shaparak.ir/onlinepg/onlinepg"
	defaultPaymentURL = "https://sep.shaparak.ir/OnlinePG/OnlinePG"
	defaultVerifyURL  = "https://sep.shaparak.ir/verifyTxnRandomSessionkey/ipg/VerifyTransaction"
	defaultReverseURL = "https://sep.shaparak.ir/verifyTxnRandomSessionkey/ipg/ReverseTransaction"
)

var _ payjet.Refunder = (*Gateway)(nil)

type Gateway struct {
	terminalID string
	tokenURL   string
	paymentURL string
	verifyURL  string
	reverseURL string
	client     *http.Client
}

type Option func(*Gateway)

// WithHTTPClient replaces the default HTTP client.
func WithHTTPClient(c *http.Client) Option {
	return func(g *Gateway) { g.client = c }
}

// WithEndpoints overrides the token, payment page, and verify URLs.
// Pass an empty string to keep the current value.
func WithEndpoints(tokenURL, paymentURL, verifyURL string) Option {
	return func(g *Gateway) {
		if tokenURL != "" {
			g.tokenURL = tokenURL
		}
		if paymentURL != "" {
			g.paymentURL = paymentURL
		}
		if verifyURL != "" {
			g.verifyURL = verifyURL
		}
	}
}

// WithReverseURL overrides the reverse (refund) endpoint.
func WithReverseURL(reverseURL string) Option {
	return func(g *Gateway) { g.reverseURL = reverseURL }
}

func New(terminalID string, opts ...Option) *Gateway {
	g := &Gateway{
		terminalID: terminalID,
		tokenURL:   defaultTokenURL,
		paymentURL: defaultPaymentURL,
		verifyURL:  defaultVerifyURL,
		reverseURL: defaultReverseURL,
		client:     payjet.DefaultHTTPClient(),
	}
	for _, o := range opts {
		o(g)
	}
	return g
}

// ---- helpers ----------------------------------------------------------------

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

// ---- request / verify -------------------------------------------------------

type tokenRequest struct {
	Action      string `json:"action"`
	TerminalId  string `json:"TerminalId"`
	Amount      int64  `json:"Amount"`
	ResNum      string `json:"ResNum"`
	RedirectUrl string `json:"RedirectUrl"`
	CellNumber  string `json:"CellNumber"`
}

type tokenResponse struct {
	Status    int    `json:"status"`
	Token     string `json:"token"`
	ErrorCode int    `json:"errorCode"`
	ErrorDesc string `json:"errorDesc"`
}

// CallbackOrderID returns the ResNum (the merchant order ID) Saman echoes back.
func (g *Gateway) CallbackOrderID(params map[string]string) string {
	return payjet.Param(params, "ResNum")
}

func (g *Gateway) Request(ctx context.Context, p *payjet.Payment) (*payjet.RequestResult, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	var result tokenResponse
	if err := g.postJSON(ctx, g.tokenURL, tokenRequest{
		Action:      "token",
		TerminalId:  g.terminalID,
		Amount:      p.Amount,
		ResNum:      p.OrderID,
		RedirectUrl: p.CallbackURL,
		CellNumber:  p.Mobile,
	}, &result); err != nil {
		return nil, payjet.Fault("saman", "request", "gateway call failed", err)
	}
	if result.Status != 1 || result.Token == "" {
		return nil, payjet.Rejected("saman", "request",
			strconv.Itoa(result.ErrorCode), result.ErrorDesc)
	}
	return &payjet.RequestResult{
		Token:      result.Token,
		PaymentURL: g.paymentURL,
		Method:     payjet.MethodPOST,
		// GetMethod=false tells the gateway to POST back to the callback URL.
		Params: map[string]string{"Token": result.Token, "GetMethod": "false"},
	}, nil
}

type verifyRequest struct {
	RefNum         string `json:"RefNum"`
	TerminalNumber string `json:"TerminalNumber"`
}

type verifyResponse struct {
	ResultCode        int    `json:"ResultCode"`
	ResultDescription string `json:"ResultDescription"`
	TransactionDetail struct {
		Rrn             string `json:"Rrn"`
		RefNum          string `json:"RefNum"`
		MaskedPan       string `json:"MaskedPan"`
		TerminalNumber  int64  `json:"TerminalNumber"`
		AffectiveAmount int64  `json:"AffectiveAmount"`
	} `json:"TransactionDetail"`
}

func (g *Gateway) Verify(ctx context.Context, p *payjet.Payment, params map[string]string) (*payjet.VerifyResult, error) {
	// Status "2" = successful payment
	status := payjet.Param(params, "Status")
	if status != "2" {
		return nil, payjet.Declined("saman", "verify", status, "")
	}
	if payjet.Param(params, "ResNum") != p.OrderID {
		return nil, payjet.Mismatch("saman", "verify", payjet.ErrOrderMismatch)
	}
	refNum := payjet.Param(params, "RefNum")
	if refNum == "" {
		return nil, payjet.Fault("saman", "verify", "no RefNum in callback", nil)
	}
	var result verifyResponse
	if err := g.postJSON(ctx, g.verifyURL, verifyRequest{
		RefNum:         refNum,
		TerminalNumber: g.terminalID,
	}, &result); err != nil {
		return nil, payjet.Fault("saman", "verify", "gateway call failed", err)
	}
	if result.ResultCode != 0 {
		return nil, payjet.Rejected("saman", "verify",
			strconv.Itoa(result.ResultCode), result.ResultDescription)
	}
	detail := result.TransactionDetail
	if strconv.FormatInt(detail.TerminalNumber, 10) != g.terminalID || detail.RefNum != refNum {
		return nil, payjet.Fault("saman", "verify",
			"verified transaction does not match the terminal or RefNum", nil)
	}
	if detail.AffectiveAmount != p.Amount {
		return nil, payjet.Mismatch("saman", "verify", payjet.ErrAmountMismatch)
	}
	return &payjet.VerifyResult{
		RefID:      result.TransactionDetail.Rrn,
		CardNumber: result.TransactionDetail.MaskedPan,
		OrderID:    p.OrderID,
		Amount:     result.TransactionDetail.AffectiveAmount,
		RawParams:  params,
	}, nil
}

// reverseResponse is ReverseTransaction's reply; it shares the verify shape.
type reverseResponse struct {
	ResultCode        int    `json:"ResultCode"`
	ResultDescription string `json:"ResultDescription"`
	Success           bool   `json:"Success"`
}

// Refund reverses the whole payment. It needs the RefNum of the verified
// callback, read from v.RawParams.
func (g *Gateway) Refund(ctx context.Context, p *payjet.Payment, v *payjet.VerifyResult) (*payjet.RefundResult, error) {
	var refNum string
	if v != nil {
		refNum = payjet.Param(v.RawParams, "RefNum")
	}
	if refNum == "" {
		return nil, payjet.Invalid("saman", "refund", "the verified callback's RefNum is required to refund")
	}
	var result reverseResponse
	if err := g.postJSON(ctx, g.reverseURL, verifyRequest{
		RefNum:         refNum,
		TerminalNumber: g.terminalID,
	}, &result); err != nil {
		return nil, payjet.Fault("saman", "refund", "gateway call failed", err)
	}
	if !result.Success {
		return nil, payjet.Rejected("saman", "refund",
			strconv.Itoa(result.ResultCode), result.ResultDescription)
	}
	return &payjet.RefundResult{OrderID: p.OrderID, Amount: p.Amount, RefID: refNum}, nil
}
