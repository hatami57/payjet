package mellat_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hatami57/microjet/core/errorx"
	"github.com/majid/payjet"
	"github.com/majid/payjet/mellat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── SOAP mock helpers ─────────────────────────────────────────────────────────

func soapResponse(method, returnVal string) string {
	return fmt.Sprintf(
		`<?xml version="1.0" encoding="utf-8"?>`+
			`<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/" xmlns:ns2="http://interfaces.core.sw.bps.com/">`+
			`<soapenv:Body><ns2:%sResponse><return>%s</return></ns2:%sResponse></soapenv:Body>`+
			`</soapenv:Envelope>`,
		method, returnVal, method,
	)
}

// soapMock dispatches by SOAPAction and returns configurable responses.
type soapMock struct {
	payReturn    string
	verifyReturn string
	settleReturn string
}

func (m *soapMock) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		action := r.Header.Get("SOAPAction")
		body, _ := io.ReadAll(r.Body)
		_ = body

		w.Header().Set("Content-Type", "text/xml; charset=UTF-8")
		switch {
		case strings.Contains(action, "bpPayRequest"):
			w.Write([]byte(soapResponse("bpPayRequest", m.payReturn)))
		case strings.Contains(action, "bpVerifyRequest"):
			w.Write([]byte(soapResponse("bpVerifyRequest", m.verifyReturn)))
		case strings.Contains(action, "bpSettleRequest"):
			w.Write([]byte(soapResponse("bpSettleRequest", m.settleReturn)))
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
		}
	}
}

func newGateway(t *testing.T, m *soapMock, opts ...mellat.Option) *mellat.Gateway {
	t.Helper()
	srv := httptest.NewServer(m.handler())
	t.Cleanup(srv.Close)
	opts = append([]mellat.Option{mellat.WithEndpoints(srv.URL, mellat.DefaultPaymentURL)}, opts...)
	return mellat.New(mellat.Config{TerminalID: 12345, Username: "user", Password: "pass"}, opts...)
}

var testPayment = &payjet.Payment{
	OrderID:     "100001",
	Amount:      200_000,
	CallbackURL: "https://shop.ir/callback",
	Description: "پرداخت تست",
}

var successCallbackParams = map[string]string{
	"ResCode":         "0",
	"RefId":           "test-ref-id-abc",
	"SaleOrderId":     "100001",
	"SaleReferenceId": "127926981246",
	"CardHolderPan":   "6104****1234",
}

// ── Request ───────────────────────────────────────────────────────────────────

func TestRequest_Success(t *testing.T) {
	gw := newGateway(t, &soapMock{payReturn: "0,test-refid-xyz"})

	res, err := gw.Request(context.Background(), testPayment)

	require.NoError(t, err)
	assert.Equal(t, "test-refid-xyz", res.Token)
	assert.Equal(t, payjet.MethodPOST, res.Method)
	assert.Equal(t, "test-refid-xyz", res.Params["RefId"])
}

func TestRequest_GatewayError(t *testing.T) {
	gw := newGateway(t, &soapMock{payReturn: "41"})

	_, err := gw.Request(context.Background(), testPayment)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "41")
}

func TestRequest_NonNumericOrderID(t *testing.T) {
	gw := newGateway(t, &soapMock{payReturn: "0,x"})

	_, err := gw.Request(context.Background(), &payjet.Payment{OrderID: "not-a-number", Amount: 1000, CallbackURL: "https://x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "numeric")
}

// ── Verify ────────────────────────────────────────────────────────────────────

func TestVerify_Success_CallsVerifyAndSettle(t *testing.T) {
	calls := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := r.Header.Get("SOAPAction")
		w.Header().Set("Content-Type", "text/xml; charset=UTF-8")
		switch {
		case strings.Contains(action, "bpVerifyRequest"):
			calls["verify"]++
			w.Write([]byte(soapResponse("bpVerifyRequest", "0")))
		case strings.Contains(action, "bpSettleRequest"):
			calls["settle"]++
			w.Write([]byte(soapResponse("bpSettleRequest", "0")))
		}
	}))
	defer srv.Close()

	gw := mellat.New(mellat.Config{TerminalID: 1, Username: "u", Password: "p"}, mellat.WithEndpoints(srv.URL, mellat.DefaultPaymentURL))
	res, err := gw.Verify(context.Background(), testPayment, successCallbackParams)

	require.NoError(t, err)
	assert.Equal(t, 1, calls["verify"], "bpVerifyRequest must be called once")
	assert.Equal(t, 1, calls["settle"], "bpSettleRequest must be called once")
	assert.Equal(t, "127926981246", res.RefID)
	assert.Equal(t, "6104****1234", res.CardNumber)
}

func TestVerify_CallbackPaymentFailed(t *testing.T) {
	gw := newGateway(t, &soapMock{})

	_, err := gw.Verify(context.Background(), testPayment, map[string]string{"ResCode": "17"}) // user cancelled
	require.Error(t, err)
	assert.Contains(t, err.Error(), "17")
}

func TestVerify_AlreadyVerified_Code43(t *testing.T) {
	gw := newGateway(t, &soapMock{verifyReturn: "43", settleReturn: "0"})

	res, err := gw.Verify(context.Background(), testPayment, successCallbackParams)
	require.NoError(t, err, "code 43 (already verified) must be treated as success")
	assert.NotEmpty(t, res.RefID)
	assert.True(t, res.AlreadyVerified)
}

func TestVerify_AlreadySettled_Code45(t *testing.T) {
	gw := newGateway(t, &soapMock{verifyReturn: "0", settleReturn: "45"})

	res, err := gw.Verify(context.Background(), testPayment, successCallbackParams)
	require.NoError(t, err, "code 45 (already settled) must be treated as success")
	assert.NotEmpty(t, res.RefID)
	assert.False(t, res.AlreadyVerified)
}

// Mellat's schema leaves the operation's fields unqualified; a namespaced
// <int:terminalId> would reach the service as a missing field.
func TestRequest_FieldsAreUnqualified(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Write([]byte(soapResponse("bpPayRequest", "0,ref")))
	}))
	t.Cleanup(srv.Close)
	gw := mellat.New(mellat.Config{TerminalID: 12345, Username: "user", Password: "pass"},
		mellat.WithEndpoints(srv.URL, mellat.DefaultPaymentURL))

	_, err := gw.Request(context.Background(), testPayment)

	require.NoError(t, err)
	assert.Contains(t, body, "<int:bpPayRequest>")
	assert.Contains(t, body, "<terminalId>12345</terminalId>")
	assert.Contains(t, body, "<orderId>100001</orderId>")
	assert.NotContains(t, body, "<int:terminalId>")
}

func TestVerify_SaleOrderIDMismatch(t *testing.T) {
	gw := newGateway(t, &soapMock{verifyReturn: "0", settleReturn: "0"})
	params := map[string]string{}
	for k, v := range successCallbackParams {
		params[k] = v
	}
	params["SaleOrderId"] = "999999"

	_, err := gw.Verify(context.Background(), testPayment, params)
	assert.ErrorIs(t, err, payjet.ErrOrderMismatch)
}

func TestVerify_TokenMismatch(t *testing.T) {
	gw := newGateway(t, &soapMock{verifyReturn: "0", settleReturn: "0"})
	p := *testPayment
	p.Token = "a-different-ref-id"

	_, err := gw.Verify(context.Background(), &p, successCallbackParams)
	assert.ErrorIs(t, err, payjet.ErrTokenMismatch)
}

func TestVerify_MatchingToken(t *testing.T) {
	gw := newGateway(t, &soapMock{verifyReturn: "0", settleReturn: "0"})
	p := *testPayment
	p.Token = successCallbackParams["RefId"]

	_, err := gw.Verify(context.Background(), &p, successCallbackParams)
	require.NoError(t, err)
}

func TestVerify_VerifyFails(t *testing.T) {
	gw := newGateway(t, &soapMock{verifyReturn: "55", settleReturn: "0"})

	_, err := gw.Verify(context.Background(), testPayment, successCallbackParams)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "55")
}

func TestVerify_SettleFails(t *testing.T) {
	gw := newGateway(t, &soapMock{verifyReturn: "0", settleReturn: "61"})

	_, err := gw.Verify(context.Background(), testPayment, successCallbackParams)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "61")
	// The payment verified, so a failed settle must read as retryable, not as
	// a decline that fails the order.
	assert.True(t, errorx.IsInternalError(err))
	assert.False(t, errorx.IsBusinessError(err))
}

func TestVerify_EmptyReturnIsFault(t *testing.T) {
	gw := newGateway(t, &soapMock{verifyReturn: "", settleReturn: "0"})

	_, err := gw.Verify(context.Background(), testPayment, successCallbackParams)
	require.Error(t, err)
	assert.True(t, errorx.IsInternalError(err))
}

func TestVerify_MissingRefId(t *testing.T) {
	gw := newGateway(t, &soapMock{})

	_, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"ResCode": "0",
		// RefId intentionally omitted
	})
	require.Error(t, err)
}

// ── Options ───────────────────────────────────────────────────────────────────

func TestWithEnglishPage(t *testing.T) {
	gw := newGateway(t, &soapMock{payReturn: "0,ref-en"},
		mellat.WithEnglishPage(),
	)
	res, err := gw.Request(context.Background(), testPayment)
	require.NoError(t, err)
	assert.Contains(t, res.PaymentURL, "enstartpay.mellat")
}

func TestWithHTTPClient(t *testing.T) {
	srv := httptest.NewServer((&soapMock{payReturn: "0,custom-client-ref"}).handler())
	defer srv.Close()

	gw := mellat.New(mellat.Config{TerminalID: 1, Username: "u", Password: "p"},
		mellat.WithEndpoints(srv.URL, mellat.DefaultPaymentURL),
		mellat.WithHTTPClient(srv.Client()),
	)
	res, err := gw.Request(context.Background(), testPayment)
	require.NoError(t, err)
	assert.Equal(t, "custom-client-ref", res.Token)
}

// ── transport failures ────────────────────────────────────────────────────────

func TestRequest_SOAPFaultIsGatewayFault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/">` +
			`<soapenv:Body><soapenv:Fault>` +
			`<faultcode>soapenv:Server</faultcode><faultstring>invalid terminal</faultstring>` +
			`</soapenv:Fault></soapenv:Body></soapenv:Envelope>`))
	}))
	t.Cleanup(srv.Close)
	gw := mellat.New(mellat.Config{TerminalID: 12345, Username: "user", Password: "pass"},
		mellat.WithEndpoints(srv.URL, mellat.DefaultPaymentURL))

	_, err := gw.Request(context.Background(), testPayment)

	require.Error(t, err)
	assert.True(t, errorx.IsInternalError(err))
	ce := errorx.GetError(err)
	require.NotNil(t, ce)
	assert.Equal(t, "mellat", ce.Subject)
	assert.Equal(t, "request", ce.Params["op"])
	// The fault detail rides in the inner cause, which microjet's HTTP error
	// middleware logs (it does not log Params).
	require.NotNil(t, ce.Inner)
	assert.Contains(t, ce.Inner.Error(), "invalid terminal")
}

// ── Refund ────────────────────────────────────────────────────────────────────

func reversalServer(t *testing.T, ret string) (*mellat.Gateway, *string) {
	t.Helper()
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Write([]byte(soapResponse("bpReversalRequest", ret)))
	}))
	t.Cleanup(srv.Close)
	return mellat.New(mellat.Config{TerminalID: 12345, Username: "user", Password: "pass"},
		mellat.WithEndpoints(srv.URL, mellat.DefaultPaymentURL)), &body
}

var verifiedPayment = &payjet.VerifyResult{RefID: "127926981246", OrderID: "100001"}

func TestRefund_Success(t *testing.T) {
	gw, body := reversalServer(t, "0")

	res, err := gw.Refund(context.Background(), testPayment, verifiedPayment)

	require.NoError(t, err)
	assert.False(t, res.AlreadyRefunded)
	assert.Equal(t, "127926981246", res.RefID)
	assert.Contains(t, *body, "<int:bpReversalRequest>")
	assert.Contains(t, *body, "<orderId>100001</orderId>")
	assert.Contains(t, *body, "<saleOrderId>100001</saleOrderId>")
	assert.Contains(t, *body, "<saleReferenceId>127926981246</saleReferenceId>")
}

func TestRefund_AlreadyReversed_Code48(t *testing.T) {
	gw, _ := reversalServer(t, "48")

	res, err := gw.Refund(context.Background(), testPayment, verifiedPayment)

	require.NoError(t, err)
	assert.True(t, res.AlreadyRefunded)
}

func TestRefund_Rejected(t *testing.T) {
	gw, _ := reversalServer(t, "45") // already settled

	_, err := gw.Refund(context.Background(), testPayment, verifiedPayment)

	ce := errorx.GetError(err)
	require.NotNil(t, ce)
	assert.Equal(t, "45", ce.Params["gatewayCode"])
	assert.Equal(t, "refund", ce.Params["op"])
}

func TestRefund_RequiresSaleReferenceID(t *testing.T) {
	gw, _ := reversalServer(t, "0")

	_, err := gw.Refund(context.Background(), testPayment, &payjet.VerifyResult{})

	assert.True(t, errorx.IsBadRequestError(err))
}
