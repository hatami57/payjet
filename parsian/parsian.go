package parsian

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/majid/payjet"
	"github.com/majid/payjet/internal/soap"
)

const (
	defaultRequestURL = "https://pec.shaparak.ir/NewIPGServices/Sale/SaleService.asmx"
	defaultVerifyURL  = "https://pec.shaparak.ir/NewIPGServices/Confirm/ConfirmService.asmx"
	defaultRefundURL  = "https://pec.shaparak.ir/NewIPGServices/Reverse/ReversalService.asmx"
	defaultPaymentURL = "https://pec.shaparak.ir/NewIPG/"

	requestNS = "https://pec.Shaparak.ir/NewIPGServices/Sale/SaleService"
	verifyNS  = "https://pec.Shaparak.ir/NewIPGServices/Confirm/ConfirmService"
	refundNS  = "https://pec.Shaparak.ir/NewIPGServices/Reversal/ReversalService"
)

type Gateway struct {
	loginAccount string
	requestURL   string
	verifyURL    string
	refundURL    string
	paymentURL   string
	client       *http.Client
}

type Option func(*Gateway)

// WithHTTPClient replaces the default HTTP client.
func WithHTTPClient(c *http.Client) Option {
	return func(g *Gateway) { g.client = c }
}

// WithEndpoints overrides the request, verify, and payment page URLs.
// Pass an empty string to keep the current value.
func WithEndpoints(requestURL, verifyURL, paymentURL string) Option {
	return func(g *Gateway) {
		if requestURL != "" {
			g.requestURL = requestURL
		}
		if verifyURL != "" {
			g.verifyURL = verifyURL
		}
		if paymentURL != "" {
			g.paymentURL = paymentURL
		}
	}
}

// WithRefundURL overrides the reversal (refund) endpoint.
func WithRefundURL(refundURL string) Option {
	return func(g *Gateway) { g.refundURL = refundURL }
}

func New(loginAccount string, opts ...Option) *Gateway {
	g := &Gateway{
		loginAccount: loginAccount,
		requestURL:   defaultRequestURL,
		verifyURL:    defaultVerifyURL,
		refundURL:    defaultRefundURL,
		paymentURL:   defaultPaymentURL,
		client:       payjet.DefaultHTTPClient(),
	}
	for _, o := range opts {
		o(g)
	}
	return g
}

// ---- SOAP helpers -----------------------------------------------------------

func xmlEscape(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

// xmlNodeValue extracts a single element's text by local name and namespace.
func xmlNodeValue(data, localName, ns string) string {
	type node struct {
		XMLName xml.Name
		Value   string `xml:",chardata"`
	}
	dec := xml.NewDecoder(strings.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if se.Name.Local == localName && se.Name.Space == ns {
			var n node
			if err := dec.DecodeElement(&n, &se); err == nil {
				return n.Value
			}
		}
	}
	return ""
}

var _ payjet.Refunder = (*Gateway)(nil)

// ---- request / verify -------------------------------------------------------

// CallbackOrderID returns the OrderId Parsian echoes back in the callback.
func (g *Gateway) CallbackOrderID(params map[string]string) string {
	return payjet.Param(params, "OrderId")
}

func (g *Gateway) Request(ctx context.Context, p *payjet.Payment) (*payjet.RequestResult, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	envelope := fmt.Sprintf(
		`<?xml version="1.0" encoding="UTF-8"?>`+
			`<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/" xmlns:sal="%s">`+
			`<soapenv:Header/><soapenv:Body>`+
			`<sal:SalePaymentRequest><sal:requestData>`+
			`<sal:LoginAccount>%s</sal:LoginAccount>`+
			`<sal:Amount>%d</sal:Amount>`+
			`<sal:OrderId>%s</sal:OrderId>`+
			`<sal:CallBackUrl>%s</sal:CallBackUrl>`+
			`<sal:AdditionalData>%s</sal:AdditionalData>`+
			`<sal:Originator></sal:Originator>`+
			`</sal:requestData></sal:SalePaymentRequest>`+
			`</soapenv:Body></soapenv:Envelope>`,
		requestNS,
		xmlEscape(g.loginAccount),
		p.Amount, xmlEscape(p.OrderID),
		xmlEscape(p.CallbackURL), xmlEscape(p.Description),
	)
	data, err := soap.Post(ctx, g.client, g.requestURL, `"SalePaymentRequest"`, envelope)
	if err != nil {
		return nil, payjet.Fault("parsian", "request", "SalePaymentRequest call failed", err)
	}
	raw := string(data)
	status := xmlNodeValue(raw, "Status", requestNS)
	token := xmlNodeValue(raw, "Token", requestNS)
	message := xmlNodeValue(raw, "Message", requestNS)

	if status != "0" || token == "" {
		return nil, payjet.Rejected("parsian", "request", status, message)
	}
	return &payjet.RequestResult{
		Token:      token,
		PaymentURL: fmt.Sprintf("%s?Token=%s", g.paymentURL, token),
		Method:     payjet.MethodGET,
	}, nil
}

// Verify confirms the payment. p.Token must be the RequestResult.Token issued
// for p: ConfirmPayment settles whichever token it is given and reports no
// amount or order, so without this check a callback carrying the token of a
// cheaper paid order would confirm that payment against p.
func (g *Gateway) Verify(ctx context.Context, p *payjet.Payment, params map[string]string) (*payjet.VerifyResult, error) {
	status := payjet.Param(params, "status")
	if status != "0" {
		return nil, payjet.Declined("parsian", "verify", status, "")
	}
	if p.Token == "" {
		return nil, payjet.Invalid("parsian", "verify", "Payment.Token is required to verify a Parsian payment")
	}
	token := payjet.Param(params, "Token")
	if token == "" {
		return nil, payjet.Fault("parsian", "verify", "no token in callback", nil)
	}
	if token != p.Token {
		return nil, payjet.Mismatch("parsian", "verify", payjet.ErrTokenMismatch)
	}
	if payjet.Param(params, "OrderId") != p.OrderID {
		return nil, payjet.Mismatch("parsian", "verify", payjet.ErrOrderMismatch)
	}
	if amount := payjet.Param(params, "Amount"); amount != "" {
		n, err := strconv.ParseInt(strings.ReplaceAll(amount, ",", ""), 10, 64)
		if err != nil || n != p.Amount {
			return nil, payjet.Mismatch("parsian", "verify", payjet.ErrAmountMismatch)
		}
	}
	envelope := fmt.Sprintf(
		`<?xml version="1.0" encoding="UTF-8"?>`+
			`<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/" xmlns:con="%s">`+
			`<soapenv:Header/><soapenv:Body>`+
			`<con:ConfirmPayment><con:requestData>`+
			`<con:LoginAccount>%s</con:LoginAccount>`+
			`<con:Token>%s</con:Token>`+
			`</con:requestData></con:ConfirmPayment>`+
			`</soapenv:Body></soapenv:Envelope>`,
		verifyNS, xmlEscape(g.loginAccount), xmlEscape(token),
	)
	data, err := soap.Post(ctx, g.client, g.verifyURL, `"ConfirmPayment"`, envelope)
	if err != nil {
		return nil, payjet.Fault("parsian", "verify", "ConfirmPayment call failed", err)
	}
	raw := string(data)
	confirmStatus := xmlNodeValue(raw, "Status", verifyNS)
	rrn := xmlNodeValue(raw, "RRN", verifyNS)

	if confirmStatus != "0" || rrn == "" {
		return nil, payjet.Rejected("parsian", "verify", confirmStatus, "")
	}
	return &payjet.VerifyResult{
		RefID:     rrn,
		OrderID:   p.OrderID,
		Amount:    p.Amount,
		RawParams: params,
	}, nil
}

// Refund reverses the whole payment. It needs p.Token, the token Request
// issued; v is not used.
func (g *Gateway) Refund(ctx context.Context, p *payjet.Payment, _ *payjet.VerifyResult) (*payjet.RefundResult, error) {
	if p.Token == "" {
		return nil, payjet.Invalid("parsian", "refund", "Payment.Token is required to refund a Parsian payment")
	}
	envelope := fmt.Sprintf(
		`<?xml version="1.0" encoding="UTF-8"?>`+
			`<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/" xmlns:rev="%s">`+
			`<soapenv:Header/><soapenv:Body>`+
			`<rev:ReversalRequest><rev:requestData>`+
			`<rev:LoginAccount>%s</rev:LoginAccount>`+
			`<rev:Token>%s</rev:Token>`+
			`</rev:requestData></rev:ReversalRequest>`+
			`</soapenv:Body></soapenv:Envelope>`,
		refundNS, xmlEscape(g.loginAccount), xmlEscape(p.Token),
	)
	data, err := soap.Post(ctx, g.client, g.refundURL, `"ReversalRequest"`, envelope)
	if err != nil {
		return nil, payjet.Fault("parsian", "refund", "ReversalRequest call failed", err)
	}
	raw := string(data)
	status := xmlNodeValue(raw, "Status", refundNS)
	if status != "0" {
		return nil, payjet.Rejected("parsian", "refund", status, xmlNodeValue(raw, "Message", refundNS))
	}
	return &payjet.RefundResult{OrderID: p.OrderID, Amount: p.Amount, RefID: p.Token}, nil
}
