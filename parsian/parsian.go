package parsian

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/majid/payjet"
)

const (
	defaultRequestURL = "https://pec.shaparak.ir/NewIPGServices/Sale/SaleService.asmx"
	defaultVerifyURL  = "https://pec.shaparak.ir/NewIPGServices/Confirm/ConfirmService.asmx"
	defaultPaymentURL = "https://pec.shaparak.ir/NewIPG/"

	requestNS = "https://pec.Shaparak.ir/NewIPGServices/Sale/SaleService"
	verifyNS  = "https://pec.Shaparak.ir/NewIPGServices/Confirm/ConfirmService"
)

type Gateway struct {
	loginAccount string
	requestURL   string
	verifyURL    string
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

func New(loginAccount string, opts ...Option) *Gateway {
	g := &Gateway{
		loginAccount: loginAccount,
		requestURL:   defaultRequestURL,
		verifyURL:    defaultVerifyURL,
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

func (g *Gateway) doSOAP(ctx context.Context, url, soapAction, envelope string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(envelope))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/xml; charset=UTF-8")
	req.Header.Set("SOAPAction", soapAction)
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// ---- request / verify -------------------------------------------------------

// CallbackOrderID returns the orderId Parsian echoes back in the callback.
func (g *Gateway) CallbackOrderID(params map[string]string) string {
	return params["orderId"]
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
	data, err := g.doSOAP(ctx, g.requestURL, `"SalePaymentRequest"`, envelope)
	if err != nil {
		return nil, err
	}
	raw := string(data)
	status := xmlNodeValue(raw, "Status", requestNS)
	token := xmlNodeValue(raw, "Token", requestNS)
	message := xmlNodeValue(raw, "Message", requestNS)

	if status != "0" || token == "" {
		return nil, &payjet.Error{Gateway: "parsian", Op: "request",
			GatewayCode: status, Message: message}
	}
	return &payjet.RequestResult{
		Token:      token,
		PaymentURL: fmt.Sprintf("%s?Token=%s", g.paymentURL, token),
		Method:     payjet.MethodGET,
	}, nil
}

func (g *Gateway) Verify(ctx context.Context, p *payjet.Payment, params map[string]string) (*payjet.VerifyResult, error) {
	if params["status"] != "0" {
		return nil, &payjet.Error{Gateway: "parsian", Op: "verify",
			GatewayCode: params["status"], Err: payjet.ErrCancelled}
	}
	if params["token"] == "" {
		return nil, &payjet.Error{Gateway: "parsian", Op: "verify", Message: "no token in callback"}
	}
	if params["orderId"] != p.OrderID {
		return nil, &payjet.Error{Gateway: "parsian", Op: "verify", Err: payjet.ErrOrderMismatch}
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
		verifyNS, xmlEscape(g.loginAccount), xmlEscape(params["token"]),
	)
	data, err := g.doSOAP(ctx, g.verifyURL, `"ConfirmPayment"`, envelope)
	if err != nil {
		return nil, err
	}
	raw := string(data)
	status := xmlNodeValue(raw, "Status", verifyNS)
	rrn := xmlNodeValue(raw, "RRN", verifyNS)

	if status != "0" || rrn == "" {
		return nil, &payjet.Error{Gateway: "parsian", Op: "verify", GatewayCode: status}
	}
	return &payjet.VerifyResult{
		RefID:     rrn,
		OrderID:   p.OrderID,
		Amount:    p.Amount,
		RawParams: params,
	}, nil
}
