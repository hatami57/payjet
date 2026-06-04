package pasargad_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	gw := newGateway(t, &pasargadMock{
		tokenCode: 0, tokenValue: "tok",
		verifyCode: 0,
	})

	res, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"status":          "success",
		"invoiceId":       testPayment.OrderID,
		"referenceNumber": "REF-98765",
		"urlId":           "url-id-123",
	})

	require.NoError(t, err)
	assert.Equal(t, "REF-98765", res.RefID)
	assert.Equal(t, testPayment.OrderID, res.OrderID)
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

	_, err := gw.Verify(context.Background(), testPayment, map[string]string{
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

	_, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"status":    "success",
		"invoiceId": testPayment.OrderID,
		"urlId":     "u",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
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
