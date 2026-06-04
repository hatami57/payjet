package parsian_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/majid/payjet"
	"github.com/majid/payjet/parsian"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── SOAP response builders ─────────────────────────────────────────────────

const requestNS = "https://pec.Shaparak.ir/NewIPGServices/Sale/SaleService"
const verifyNS = "https://pec.Shaparak.ir/NewIPGServices/Confirm/ConfirmService"

func saleResponse(status, token, message string) string {
	return fmt.Sprintf(`<?xml version="1.0"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <SalePaymentRequestResponse xmlns="%s">
      <SalePaymentRequestResult>
        <Status>%s</Status>
        <Token>%s</Token>
        <Message>%s</Message>
      </SalePaymentRequestResult>
    </SalePaymentRequestResponse>
  </soap:Body>
</soap:Envelope>`, requestNS, status, token, message)
}

func confirmResponse(status, rrn, token string) string {
	return fmt.Sprintf(`<?xml version="1.0"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <ConfirmPaymentResponse xmlns="%s">
      <ConfirmPaymentResult>
        <Status>%s</Status>
        <RRN>%s</RRN>
        <Token>%s</Token>
      </ConfirmPaymentResult>
    </ConfirmPaymentResponse>
  </soap:Body>
</soap:Envelope>`, verifyNS, status, rrn, token)
}

// ── mock & factory ────────────────────────────────────────────────────────────

type parsianMock struct {
	saleStatus    string
	saleToken     string
	saleMsg       string
	confirmStatus string
	confirmRRN    string
}

func (m *parsianMock) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/xml; charset=UTF-8")

		if strings.Contains(string(body), "SalePaymentRequest") {
			w.Write([]byte(saleResponse(m.saleStatus, m.saleToken, m.saleMsg)))
		} else {
			w.Write([]byte(confirmResponse(m.confirmStatus, m.confirmRRN, m.saleToken)))
		}
	}))
}

func newGateway(t *testing.T, m *parsianMock) *parsian.Gateway {
	t.Helper()
	srv := m.server(t)
	t.Cleanup(srv.Close)
	return parsian.New("test-login-account",
		parsian.WithEndpoints(srv.URL, srv.URL, "https://pec.shaparak.ir/NewIPG/"),
	)
}

var testPayment = &payjet.Payment{
	OrderID:     "order-55",
	Amount:      600_000,
	CallbackURL: "https://shop.ir/callback",
	Description: "پرداخت",
}

// ── Request ───────────────────────────────────────────────────────────────────

func TestRequest_Success(t *testing.T) {
	gw := newGateway(t, &parsianMock{saleStatus: "0", saleToken: "parsian-token-xyz"})

	res, err := gw.Request(context.Background(), testPayment)

	require.NoError(t, err)
	assert.Equal(t, "parsian-token-xyz", res.Token)
	assert.Equal(t, payjet.MethodGET, res.Method)
	assert.Contains(t, res.PaymentURL, "parsian-token-xyz")
}

func TestRequest_SendsLoginAccountInSOAP(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("Content-Type", "text/xml")
		w.Write([]byte(saleResponse("0", "tok", "OK")))
	}))
	defer srv.Close()

	gw := parsian.New("MY-LOGIN-ACCOUNT", parsian.WithEndpoints(srv.URL, srv.URL, ""))
	_, _ = gw.Request(context.Background(), testPayment)

	assert.Contains(t, body, "MY-LOGIN-ACCOUNT")
	assert.Contains(t, body, testPayment.OrderID)
	assert.Contains(t, body, fmt.Sprintf("%d", testPayment.Amount))
}

func TestRequest_GatewayError(t *testing.T) {
	gw := newGateway(t, &parsianMock{saleStatus: "-1", saleMsg: "invalid merchant"})

	_, err := gw.Request(context.Background(), testPayment)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "-1")
}

func TestRequest_EmptyToken(t *testing.T) {
	gw := newGateway(t, &parsianMock{saleStatus: "0", saleToken: ""})

	_, err := gw.Request(context.Background(), testPayment)
	require.Error(t, err)
}

// ── Verify ────────────────────────────────────────────────────────────────────

func TestVerify_Success(t *testing.T) {
	gw := newGateway(t, &parsianMock{
		saleStatus: "0", saleToken: "parsian-token",
		confirmStatus: "0", confirmRRN: "123456789012",
	})

	res, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"status":  "0",
		"token":   "parsian-token",
		"orderId": testPayment.OrderID,
		"amount":  fmt.Sprintf("%d", testPayment.Amount),
	})

	require.NoError(t, err)
	assert.Equal(t, "123456789012", res.RefID)
}

func TestVerify_CallbackStatusFailed(t *testing.T) {
	gw := newGateway(t, &parsianMock{})

	_, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"status":  "-1",
		"token":   "any",
		"orderId": testPayment.OrderID,
	})
	require.Error(t, err)
}

func TestVerify_OrderIDMismatch(t *testing.T) {
	gw := newGateway(t, &parsianMock{})

	_, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"status":  "0",
		"token":   "tok",
		"orderId": "wrong-order",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, payjet.ErrOrderMismatch)
}

func TestVerify_MissingToken(t *testing.T) {
	gw := newGateway(t, &parsianMock{})

	_, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"status":  "0",
		"orderId": testPayment.OrderID,
	})
	require.Error(t, err)
}

func TestVerify_ConfirmFails(t *testing.T) {
	gw := newGateway(t, &parsianMock{
		saleStatus: "0", saleToken: "tok",
		confirmStatus: "-6", confirmRRN: "",
	})

	_, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"status":  "0",
		"token":   "tok",
		"orderId": testPayment.OrderID,
	})
	require.Error(t, err)
}
