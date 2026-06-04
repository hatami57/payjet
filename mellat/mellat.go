package mellat

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/majid/payjet"
	"github.com/majid/payjet/internal/soap"
)

const (
	defaultSOAPEndpoint = "https://bpm.shaparak.ir/pgwchannel/services/pgw"
	defaultPaymentURL   = "https://bpm.shaparak.ir/pgwchannel/startpay.mellat"
	englishPaymentURL   = "https://bpm.shaparak.ir/pgwchannel/enstartpay.mellat"
	soapNS              = "http://interfaces.core.sw.bps.com/"

	// DefaultPaymentURL is the production Persian-language payment page.
	DefaultPaymentURL = defaultPaymentURL
)

type Gateway struct {
	terminalID   int64
	username     string
	password     string
	soapEndpoint string
	paymentURL   string
	client       *http.Client
}

type Option func(*Gateway)

// WithEnglishPage switches the redirect to the English-language payment page.
func WithEnglishPage() Option {
	return func(g *Gateway) { g.paymentURL = englishPaymentURL }
}

// WithHTTPClient replaces the default HTTP client.
func WithHTTPClient(c *http.Client) Option {
	return func(g *Gateway) { g.client = c }
}

// WithEndpoints overrides the SOAP endpoint and/or the payment page URL.
// Pass an empty string to keep the current value.
func WithEndpoints(soapEndpoint, paymentURL string) Option {
	return func(g *Gateway) {
		if soapEndpoint != "" {
			g.soapEndpoint = soapEndpoint
		}
		if paymentURL != "" {
			g.paymentURL = paymentURL
		}
	}
}

// Config holds the merchant credentials for a Mellat terminal.
type Config struct {
	TerminalID int64
	Username   string
	Password   string
}

func New(cfg Config, opts ...Option) *Gateway {
	g := &Gateway{
		terminalID:   cfg.TerminalID,
		username:     cfg.Username,
		password:     cfg.Password,
		soapEndpoint: defaultSOAPEndpoint,
		paymentURL:   defaultPaymentURL,
		client:       payjet.DefaultHTTPClient(),
	}
	for _, o := range opts {
		o(g)
	}
	return g
}

// ---- SOAP helpers -----------------------------------------------------------

// soapEnvelope is a minimal struct to extract the <return> value from any method response.
type soapEnvelope struct {
	XMLName xml.Name `xml:"Envelope"`
	Body    struct {
		Response struct {
			Return string `xml:"return"`
		} `xml:",any"`
	} `xml:"Body"`
}

func xmlEscape(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

func (g *Gateway) call(ctx context.Context, action, innerXML string) (string, error) {
	envelope := fmt.Sprintf(
		`<?xml version="1.0" encoding="UTF-8"?>`+
			`<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/" xmlns:int="%s">`+
			`<soapenv:Header/><soapenv:Body>%s</soapenv:Body></soapenv:Envelope>`,
		soapNS, innerXML,
	)
	data, err := soap.Post(ctx, g.client, g.soapEndpoint, soapNS+action, envelope)
	if err != nil {
		return "", err
	}
	var env soapEnvelope
	if err := xml.Unmarshal(data, &env); err != nil {
		return "", fmt.Errorf("mellat: failed to parse SOAP response: %w", err)
	}
	return strings.TrimSpace(env.Body.Response.Return), nil
}

// ---- request / verify -------------------------------------------------------

// CallbackOrderID returns the SaleOrderId (the merchant order ID) Mellat echoes back.
func (g *Gateway) CallbackOrderID(params map[string]string) string {
	return params["SaleOrderId"]
}

func (g *Gateway) Request(ctx context.Context, p *payjet.Payment) (*payjet.RequestResult, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	orderID, err := strconv.ParseInt(p.OrderID, 10, 64)
	if err != nil {
		return nil, payjet.Invalid("mellat", "request",
			fmt.Sprintf("OrderID must be numeric, got %q", p.OrderID))
	}
	now := time.Now()
	body := fmt.Sprintf(
		`<int:bpPayRequest>`+
			`<int:terminalId>%d</int:terminalId>`+
			`<int:userName>%s</int:userName>`+
			`<int:userPassword>%s</int:userPassword>`+
			`<int:orderId>%d</int:orderId>`+
			`<int:amount>%d</int:amount>`+
			`<int:localDate>%s</int:localDate>`+
			`<int:localTime>%s</int:localTime>`+
			`<int:additionalData>%s</int:additionalData>`+
			`<int:callBackUrl>%s</int:callBackUrl>`+
			`<int:payerId>0</int:payerId>`+
			`</int:bpPayRequest>`,
		g.terminalID, xmlEscape(g.username), xmlEscape(g.password),
		orderID, p.Amount,
		now.Format("20060102"), now.Format("150405"),
		xmlEscape(p.Description), xmlEscape(p.CallbackURL),
	)
	raw, err := g.call(ctx, "bpPayRequest", body)
	if err != nil {
		return nil, err
	}
	parts := strings.SplitN(raw, ",", 2)
	if len(parts) != 2 {
		return nil, payjet.Fault("mellat", "request",
			fmt.Sprintf("unexpected bpPayRequest response: %q", raw), nil)
	}
	if code := strings.TrimSpace(parts[0]); code != "0" {
		return nil, payjet.Rejected("mellat", "request", code, "")
	}
	refID := strings.TrimSpace(parts[1])
	return &payjet.RequestResult{
		Token:      refID,
		PaymentURL: g.paymentURL,
		Method:     payjet.MethodPOST,
		Params:     map[string]string{"RefId": refID},
	}, nil
}

func (g *Gateway) Verify(ctx context.Context, p *payjet.Payment, params map[string]string) (*payjet.VerifyResult, error) {
	if params["ResCode"] != "0" {
		return nil, payjet.Declined("mellat", "verify", params["ResCode"], "")
	}
	if params["RefId"] == "" || params["SaleOrderId"] == "" {
		return nil, payjet.Fault("mellat", "verify", "incomplete callback params", nil)
	}
	orderID, _ := strconv.ParseInt(p.OrderID, 10, 64)
	saleOrderID, err := strconv.ParseInt(params["SaleOrderId"], 10, 64)
	if err != nil {
		return nil, payjet.Fault("mellat", "verify", "invalid SaleOrderId", err)
	}
	saleRefID, err := strconv.ParseInt(params["SaleReferenceId"], 10, 64)
	if err != nil {
		return nil, payjet.Fault("mellat", "verify", "invalid SaleReferenceId", err)
	}
	if err := g.verify(ctx, orderID, saleOrderID, saleRefID); err != nil {
		return nil, err
	}
	if err := g.settle(ctx, orderID, saleOrderID, saleRefID); err != nil {
		return nil, err
	}
	return &payjet.VerifyResult{
		RefID:      params["SaleReferenceId"],
		CardNumber: params["CardHolderPan"],
		OrderID:    p.OrderID,
		Amount:     p.Amount,
		RawParams:  params,
	}, nil
}

func (g *Gateway) verify(ctx context.Context, orderID, saleOrderID, saleRefID int64) error {
	body := fmt.Sprintf(
		`<int:bpVerifyRequest>`+
			`<int:terminalId>%d</int:terminalId>`+
			`<int:userName>%s</int:userName>`+
			`<int:userPassword>%s</int:userPassword>`+
			`<int:orderId>%d</int:orderId>`+
			`<int:saleOrderId>%d</int:saleOrderId>`+
			`<int:saleReferenceId>%d</int:saleReferenceId>`+
			`</int:bpVerifyRequest>`,
		g.terminalID, xmlEscape(g.username), xmlEscape(g.password),
		orderID, saleOrderID, saleRefID,
	)
	code, err := g.call(ctx, "bpVerifyRequest", body)
	if err != nil {
		return err
	}
	if code != "0" && code != "43" { // 43 = already verified
		return payjet.Rejected("mellat", "verify", code, "")
	}
	return nil
}

func (g *Gateway) settle(ctx context.Context, orderID, saleOrderID, saleRefID int64) error {
	body := fmt.Sprintf(
		`<int:bpSettleRequest>`+
			`<int:terminalId>%d</int:terminalId>`+
			`<int:userName>%s</int:userName>`+
			`<int:userPassword>%s</int:userPassword>`+
			`<int:orderId>%d</int:orderId>`+
			`<int:saleOrderId>%d</int:saleOrderId>`+
			`<int:saleReferenceId>%d</int:saleReferenceId>`+
			`</int:bpSettleRequest>`,
		g.terminalID, xmlEscape(g.username), xmlEscape(g.password),
		orderID, saleOrderID, saleRefID,
	)
	code, err := g.call(ctx, "bpSettleRequest", body)
	if err != nil {
		return err
	}
	if code != "0" && code != "45" { // 45 = already settled
		return payjet.Rejected("mellat", "settle", code, "")
	}
	return nil
}
