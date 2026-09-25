package pasargad_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hatami57/microjet/core/errorx"

	"github.com/majid/payjet"
	"github.com/majid/payjet/pasargad"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── mock server ───────────────────────────────────────────────────────────────

type pasargadMock struct {
	tokenCode     int
	tokenValue    string
	tokenMsg      string
	purchaseCode  int
	purchaseURLID string
	purchaseURL   string
	purchaseMsg   string
	verifyCode    int
	verifyMsg     string

	gotVerify map[string]any // body of the last Verify-Payment call
}

func (m *pasargadMock) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/Token/GetToken"):
			json.NewEncoder(w).Encode(map[string]interface{}{
				"Token":      m.tokenValue,
				"ResultCode": m.tokenCode,
				"ResultMsg":  m.tokenMsg,
			})
		case strings.HasSuffix(r.URL.Path, "/Api/Payment/purchase"):
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ResultCode": m.purchaseCode,
				"ResultMsg":  m.purchaseMsg,
				"Data": map[string]interface{}{
					"UrlId": m.purchaseURLID,
					"Url":   m.purchaseURL,
				},
			})
		case strings.HasSuffix(r.URL.Path, "/Api/Payment/Verify-Payment"):
			_ = json.NewDecoder(r.Body).Decode(&m.gotVerify)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ResultCode": m.verifyCode,
				"ResultMsg":  m.verifyMsg,
			})
		default:
			http.NotFound(w, r)
		}
	}))
}

func newGateway(t *testing.T, m *pasargadMock) *pasargad.Gateway {
	t.Helper()
	srv := m.server(t)
	t.Cleanup(srv.Close)
	return pasargad.New(pasargad.Config{BaseURL: srv.URL + "/", TerminalNumber: "TERM001", Username: "user", Password: "pass"})
}

var testPayment = &payjet.Payment{
	OrderID:     "order-pg-1",
	Amount:      250_000,
	CallbackURL: "https://shop.ir/callback",
	Mobile:      "09100000000",
	Email:       "buyer@shop.ir",
	Description: "خرید",
}

// withToken returns testPayment carrying the UrlId Request issued for it, as a
// caller passes it to Verify.
func withToken(tok string) *payjet.Payment {
	p := *testPayment
	p.Token = tok
	return &p
}

// ── Request ───────────────────────────────────────────────────────────────────

func TestRequest_Success(t *testing.T) {
	gw := newGateway(t, &pasargadMock{
		tokenCode: 0, tokenValue: "bearer-token-abc",
		purchaseCode: 0, purchaseURLID: "url-id-123", purchaseURL: "https://ipg.pasargad.ir/pay/url-id-123",
	})

	res, err := gw.Request(context.Background(), testPayment)

	require.NoError(t, err)
	assert.Equal(t, "url-id-123", res.Token)
	assert.Equal(t, "https://ipg.pasargad.ir/pay/url-id-123", res.PaymentURL)
	assert.Equal(t, payjet.MethodGET, res.Method)
}

func TestRequest_SendsBearerToken(t *testing.T) {
	var authHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/Token/GetToken"):
			json.NewEncoder(w).Encode(map[string]interface{}{"Token": "my-bearer", "ResultCode": 0})
		default:
			authHeader = r.Header.Get("Authorization")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ResultCode": 0,
				"Data":       map[string]interface{}{"UrlId": "u", "Url": "http://pay"},
			})
		}
	}))
	defer srv.Close()

	gw := pasargad.New(pasargad.Config{BaseURL: srv.URL + "/", TerminalNumber: "T", Username: "u", Password: "p"})
	_, _ = gw.Request(context.Background(), testPayment)
	assert.Equal(t, "Bearer my-bearer", authHeader)
}

func TestRequest_AuthFailed(t *testing.T) {
	gw := newGateway(t, &pasargadMock{tokenCode: 403, tokenMsg: "invalid credentials"})

	_, err := gw.Request(context.Background(), testPayment)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
}

func TestRequest_PurchaseFailed(t *testing.T) {
	gw := newGateway(t, &pasargadMock{
		tokenCode: 0, tokenValue: "tok",
		purchaseCode: 400, purchaseMsg: "invalid terminal",
	})

	_, err := gw.Request(context.Background(), testPayment)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "400")
}

// ── Verify ────────────────────────────────────────────────────────────────────

func TestVerify_Success(t *testing.T) {
	m := &pasargadMock{
		tokenCode: 0, tokenValue: "tok",
		verifyCode: 0,
	}
	gw := newGateway(t, m)

	// Pasargad's callback carries no UrlId; Verify sends the one from Request.
	res, err := gw.Verify(context.Background(), withToken("url-id-123"), map[string]string{
		"status":          "success",
		"invoiceId":       testPayment.OrderID,
		"referenceNumber": "REF-98765",
		"trackId":         "7",
	})

	require.NoError(t, err)
	assert.Equal(t, "REF-98765", res.RefID)
	assert.Equal(t, testPayment.OrderID, res.OrderID)
	assert.Equal(t, "url-id-123", m.gotVerify["UrlId"])
	assert.Equal(t, testPayment.OrderID, m.gotVerify["Invoice"])
}

func TestVerify_StatusFailed(t *testing.T) {
	gw := newGateway(t, &pasargadMock{tokenCode: 0, tokenValue: "tok"})

	_, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"status":    "failed",
		"invoiceId": testPayment.OrderID,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed")
}

func TestVerify_OrderIDMismatch(t *testing.T) {
	gw := newGateway(t, &pasargadMock{tokenCode: 0, tokenValue: "tok"})

	_, err := gw.Verify(context.Background(), withToken("u"), map[string]string{
		"status":    "success",
		"invoiceId": "wrong-order",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, payjet.ErrOrderMismatch)
}

func TestVerify_VerifyCallFailed(t *testing.T) {
	gw := newGateway(t, &pasargadMock{
		tokenCode: 0, tokenValue: "tok",
		verifyCode: 500, verifyMsg: "server error",
	})

	_, err := gw.Verify(context.Background(), withToken("u"), map[string]string{
		"status":    "success",
		"invoiceId": testPayment.OrderID,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}

func TestVerify_RequiresPaymentToken(t *testing.T) {
	gw := newGateway(t, &pasargadMock{tokenCode: 0, tokenValue: "tok"})

	_, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"status":    "success",
		"invoiceId": testPayment.OrderID,
	})
	require.Error(t, err)
	assert.True(t, errorx.IsBadRequestError(err))
}

// ── Options ───────────────────────────────────────────────────────────────────

func TestWithPaths_Override(t *testing.T) {
	var hitPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/auth"):
			json.NewEncoder(w).Encode(map[string]interface{}{"Token": "t", "ResultCode": 0})
		default:
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ResultCode": 0,
				"Data":       map[string]interface{}{"UrlId": "u", "Url": "http://pay"},
			})
		}
	}))
	defer srv.Close()

	gw := pasargad.New(pasargad.Config{BaseURL: srv.URL + "/", TerminalNumber: "T", Username: "u", Password: "p"},
		pasargad.WithPaths("auth", "Api/Payment/buy", ""),
	)
	_, _ = gw.Request(context.Background(), testPayment)
	assert.Equal(t, "/Api/Payment/buy", hitPath)
}

// ── Refund ────────────────────────────────────────────────────────────────────

// reverseServer serves GetToken and the reverse path, answering the latter with resp.
func reverseServer(t *testing.T, resp map[string]any) (*pasargad.Gateway, *map[string]any, *string) {
	t.Helper()
	var sent map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/Token/GetToken"):
			json.NewEncoder(w).Encode(map[string]any{"Token": "bearer-1", "ResultCode": 0})
		case strings.HasSuffix(r.URL.Path, "/Api/Payment/Reverse-Transactions"):
			auth = r.Header.Get("Authorization")
			_ = json.NewDecoder(r.Body).Decode(&sent)
			json.NewEncoder(w).Encode(resp)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	gw := pasargad.New(pasargad.Config{BaseURL: srv.URL + "/", TerminalNumber: "TERM001", Username: "u", Password: "p"})
	return gw, &sent, &auth
}

func TestRefund_Success(t *testing.T) {
	gw, sent, auth := reverseServer(t, map[string]any{"IsSuccess": true, "Message": "OK"})

	res, err := gw.Refund(context.Background(), withToken("url-id-1"), nil)

	require.NoError(t, err)
	assert.Equal(t, testPayment.Amount, res.Amount)
	assert.Equal(t, "Bearer bearer-1", *auth)
	assert.Equal(t, testPayment.OrderID, (*sent)["Invoice"])
	assert.Equal(t, "url-id-1", (*sent)["UrlId"])
}

func TestRefund_ResultCodeShape(t *testing.T) {
	gw, _, _ := reverseServer(t, map[string]any{"ResultCode": 0, "ResultMsg": "Successful"})

	_, err := gw.Refund(context.Background(), withToken("url-id-1"), nil)

	require.NoError(t, err)
}

func TestRefund_Rejected(t *testing.T) {
	gw, _, _ := reverseServer(t, map[string]any{"ResultCode": 13046, "ResultMsg": "reverse not allowed"})

	_, err := gw.Refund(context.Background(), withToken("url-id-1"), nil)

	ce := errorx.GetError(err)
	require.NotNil(t, ce)
	assert.Equal(t, "13046", ce.Params["gatewayCode"])
	assert.Equal(t, "reverse not allowed", ce.Message)
}

func TestRefund_IsSuccessFalse(t *testing.T) {
	gw, _, _ := reverseServer(t, map[string]any{"IsSuccess": false, "Message": "failed"})

	_, err := gw.Refund(context.Background(), withToken("url-id-1"), nil)

	require.Error(t, err)
	assert.True(t, errorx.IsBusinessError(err))
}

func TestRefund_RequiresToken(t *testing.T) {
	gw, _, _ := reverseServer(t, map[string]any{"IsSuccess": true})

	_, err := gw.Refund(context.Background(), testPayment, nil)

	assert.True(t, errorx.IsBadRequestError(err))
}
