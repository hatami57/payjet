package idpay_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/majid/payjet"
	"github.com/majid/payjet/idpay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── mock server ───────────────────────────────────────────────────────────────

func newServer(t *testing.T, requestBody, verifyBody interface{}) (*httptest.Server, *idpay.Gateway) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1.1/payment":
			json.NewEncoder(w).Encode(requestBody)
		case "/v1.1/payment/verify":
			json.NewEncoder(w).Encode(verifyBody)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	gw := idpay.New("test-api-key",
		idpay.WithEndpoints(srv.URL+"/v1.1/payment", srv.URL+"/v1.1/payment/verify"),
	)
	return srv, gw
}

var testPayment = &payjet.Payment{
	OrderID:     "order-99",
	Amount:      300_000,
	CallbackURL: "https://shop.ir/callback",
	Mobile:      "09130000000",
	Email:       "user@example.com",
	Description: "خرید",
}

// ── Request ───────────────────────────────────────────────────────────────────

func TestRequest_Success(t *testing.T) {
	_, gw := newServer(t,
		map[string]interface{}{"id": "tx-idpay-001", "link": "https://idpay.ir/p/ws/tx-idpay-001"},
		nil,
	)
	res, err := gw.Request(context.Background(), testPayment)

	require.NoError(t, err)
	assert.Equal(t, "tx-idpay-001", res.Token)
	assert.Equal(t, "https://idpay.ir/p/ws/tx-idpay-001", res.PaymentURL)
	assert.Equal(t, payjet.MethodGET, res.Method)
}

func TestRequest_SendsCorrectHeaders(t *testing.T) {
	var apiKey, sandbox string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey = r.Header.Get("X-API-KEY")
		sandbox = r.Header.Get("X-SANDBOX")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"id": "x", "link": "http://x"})
	}))
	defer srv.Close()

	gw := idpay.New("my-secret-key",
		idpay.WithEndpoints(srv.URL, srv.URL),
		idpay.WithSandbox(),
	)
	_, _ = gw.Request(context.Background(), testPayment)

	assert.Equal(t, "my-secret-key", apiKey)
	assert.Equal(t, "1", sandbox)
}

func TestRequest_SendsCorrectBody(t *testing.T) {
	var body map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"id": "z", "link": "http://z"})
	}))
	defer srv.Close()
	gw := idpay.New("k", idpay.WithEndpoints(srv.URL, srv.URL))
	_, _ = gw.Request(context.Background(), testPayment)

	assert.Equal(t, testPayment.OrderID, body["order_id"])
	assert.Equal(t, float64(testPayment.Amount), body["amount"])
	assert.Equal(t, testPayment.CallbackURL, body["callback"])
	assert.Equal(t, testPayment.Mobile, body["phone"])
	assert.Equal(t, testPayment.Email, body["mail"])
}

func TestRequest_GatewayError(t *testing.T) {
	_, gw := newServer(t,
		map[string]interface{}{"error_code": 11, "error_message": "api key not found"},
		nil,
	)
	_, err := gw.Request(context.Background(), testPayment)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "11")
}

// ── Verify ────────────────────────────────────────────────────────────────────

func TestVerify_Success(t *testing.T) {
	_, gw := newServer(t,
		map[string]interface{}{"id": "tx-1", "link": "http://x"},
		map[string]interface{}{
			"status":   100,
			"track_id": 987654321,
			"payment": map[string]interface{}{
				"card_no": "6037****5678",
			},
		},
	)
	res, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"status":   "10",
		"id":       "tx-1",
		"order_id": testPayment.OrderID,
	})

	require.NoError(t, err)
	assert.Equal(t, "987654321", res.RefID)
	assert.Equal(t, "6037****5678", res.CardNumber)
}

func TestVerify_CallbackStatusNotReady(t *testing.T) {
	_, gw := newServer(t, nil, nil)

	_, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"status": "2", // failed
		"id":     "tx-fail",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, payjet.ErrCancelled)
}

func TestVerify_GatewayError(t *testing.T) {
	_, gw := newServer(t,
		nil,
		map[string]interface{}{"status": 403, "error_message": "forbidden"},
	)
	_, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"status":   "10",
		"id":       "tx-bad",
		"order_id": testPayment.OrderID,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
}

// ── Options ───────────────────────────────────────────────────────────────────

func TestWithSandbox_SetsHeader(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-SANDBOX")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"id": "s", "link": "http://s"})
	}))
	defer srv.Close()

	gw := idpay.New("k", idpay.WithEndpoints(srv.URL, srv.URL), idpay.WithSandbox())
	_, _ = gw.Request(context.Background(), testPayment)
	assert.Equal(t, "1", got)
}

func TestWithoutSandbox_NoHeader(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-SANDBOX")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"id": "s", "link": "http://s"})
	}))
	defer srv.Close()

	gw := idpay.New("k", idpay.WithEndpoints(srv.URL, srv.URL))
	_, _ = gw.Request(context.Background(), testPayment)
	assert.Empty(t, got)
}
