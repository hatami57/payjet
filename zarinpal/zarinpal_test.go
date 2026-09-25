package zarinpal_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hatami57/microjet/core/errorx"
	"github.com/majid/payjet"
	"github.com/majid/payjet/zarinpal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── helpers ───────────────────────────────────────────────────────────────────

type zarinpalMock struct {
	requestCode      int
	requestAuthority string
	verifyCode       int
	verifyRefID      int64
	verifyCardPan    string
}

func (m *zarinpalMock) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/pg/v4/payment/request.json":
			var body map[string]interface{}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"code":      m.requestCode,
					"authority": m.requestAuthority,
					"message":   "OK",
				},
				"errors": []interface{}{},
			})
		case "/pg/v4/payment/verify.json":
			var body map[string]interface{}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"code":     m.verifyCode,
					"ref_id":   m.verifyRefID,
					"card_pan": m.verifyCardPan,
					"message":  "OK",
				},
				"errors": []interface{}{},
			})
		default:
			http.NotFound(w, r)
		}
	}))
}

func newGateway(t *testing.T, m *zarinpalMock, opts ...zarinpal.Option) *zarinpal.Gateway {
	t.Helper()
	srv := m.server(t)
	t.Cleanup(srv.Close)
	base := srv.URL + "/pg/v4/payment/"
	opts = append([]zarinpal.Option{
		zarinpal.WithEndpoints(base+"request.json", base+"verify.json", srv.URL+"/pg/StartPay/"),
	}, opts...)
	return zarinpal.New("test-merchant-id", opts...)
}

var testPayment = &payjet.Payment{
	OrderID:     "order-1",
	Amount:      500_000,
	CallbackURL: "https://myshop.ir/callback",
	Description: "خرید محصول",
}

// ── Request ───────────────────────────────────────────────────────────────────

func TestRequest_Success(t *testing.T) {
	gw := newGateway(t, &zarinpalMock{requestCode: 100, requestAuthority: "A000000test"})

	res, err := gw.Request(context.Background(), testPayment)

	require.NoError(t, err)
	assert.Equal(t, "A000000test", res.Token)
	assert.Equal(t, payjet.MethodGET, res.Method)
	assert.Contains(t, res.PaymentURL, "A000000test")
}

func TestRequest_SendsCorrectBody(t *testing.T) {
	var captured map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"code": 100, "authority": "auth-x"},
		})
	}))
	defer srv.Close()

	gw := zarinpal.New("AAAA-BBBB",
		zarinpal.WithEndpoints(srv.URL, srv.URL, srv.URL+"/pay/"),
	)
	p := &payjet.Payment{
		OrderID: "order-x", Amount: 200_000, CallbackURL: "https://shop.ir/cb",
		Description: "Test", Mobile: "09120000000", Email: "a@b.com",
	}
	_, _ = gw.Request(context.Background(), p)

	assert.Equal(t, "AAAA-BBBB", captured["merchant_id"])
	assert.Equal(t, float64(200_000), captured["amount"])
	assert.Equal(t, "https://shop.ir/cb", captured["callback_url"])
	assert.Equal(t, "09120000000", captured["mobile"])
	assert.Equal(t, "a@b.com", captured["email"])
}

func TestRequest_GatewayError(t *testing.T) {
	gw := newGateway(t, &zarinpalMock{requestCode: -9, requestAuthority: ""})

	_, err := gw.Request(context.Background(), testPayment)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "-9")
}

// ── Verify ────────────────────────────────────────────────────────────────────

func TestVerify_Success(t *testing.T) {
	gw := newGateway(t, &zarinpalMock{
		requestCode: 100, requestAuthority: "A000test",
		verifyCode: 100, verifyRefID: 123456789, verifyCardPan: "6037****1234",
	})

	res, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"Status":    "OK",
		"Authority": "A000test",
	})

	require.NoError(t, err)
	assert.Equal(t, "123456789", res.RefID)
	assert.Equal(t, "6037****1234", res.CardNumber)
}

func TestVerify_AlreadyVerified_Code101(t *testing.T) {
	gw := newGateway(t, &zarinpalMock{verifyCode: 101, verifyRefID: 9999})

	res, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"Status": "OK", "Authority": "A-any",
	})

	require.NoError(t, err, "code 101 (already verified) must be treated as success")
	assert.Equal(t, "9999", res.RefID)
	assert.True(t, res.AlreadyVerified)
}

func TestVerify_UserCancelled(t *testing.T) {
	gw := newGateway(t, &zarinpalMock{})

	_, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"Status": "NOK", "Authority": "A-any",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cancelled")
}

func TestVerify_GatewayError(t *testing.T) {
	gw := newGateway(t, &zarinpalMock{verifyCode: -22})

	_, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"Status": "OK", "Authority": "A-fail",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "-22")
}

// ── Options ───────────────────────────────────────────────────────────────────

func TestWithSandbox_UsesSandboxURLs(t *testing.T) {
	var hitURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitURL = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"code": 100, "authority": "sandbox-auth"},
		})
	}))
	defer srv.Close()

	// override sandbox URLs to point at our test server
	gw := zarinpal.New("test-id",
		zarinpal.WithSandbox(),
		zarinpal.WithEndpoints(srv.URL+"/sandbox/request", srv.URL+"/sandbox/verify", srv.URL+"/sandbox/pay/"),
	)
	_, _ = gw.Request(context.Background(), testPayment)
	assert.Equal(t, "/sandbox/request", hitURL)
}

func TestWithHTTPClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": map[string]interface{}{"code": 100, "authority": "custom-client-auth"},
		})
	}))
	defer srv.Close()

	gw := zarinpal.New("id",
		zarinpal.WithEndpoints(srv.URL, srv.URL, srv.URL+"/"),
		zarinpal.WithHTTPClient(srv.Client()),
	)
	res, err := gw.Request(context.Background(), testPayment)
	require.NoError(t, err)
	assert.Equal(t, "custom-client-auth", res.Token)
}

func TestRequest_MalformedResponseIsGatewayFault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>bad gateway</html>"))
	}))
	t.Cleanup(srv.Close)
	gw := zarinpal.New("test-merchant-id", zarinpal.WithEndpoints(srv.URL, srv.URL, srv.URL))

	_, err := gw.Request(context.Background(), testPayment)

	require.Error(t, err)
	assert.True(t, errorx.IsInternalError(err))
	ce := errorx.GetError(err)
	require.NotNil(t, ce)
	assert.Equal(t, "zarinpal", ce.Subject)
	assert.Equal(t, "request", ce.Params["op"])
	assert.NotNil(t, ce.Inner)
}

// errorServer answers every call the way Zarinpal reports a failure: an HTTP
// error status, "data" as an empty array, and the code under "errors".
func errorServer(t *testing.T, status int, errors string) *zarinpal.Gateway {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(`{"data":[],"errors":` + errors + `}`))
	}))
	t.Cleanup(srv.Close)
	return zarinpal.New("test-merchant-id", zarinpal.WithEndpoints(srv.URL, srv.URL, srv.URL))
}

func TestRequest_ErrorEnvelopeIsRejection(t *testing.T) {
	gw := errorServer(t, http.StatusBadRequest,
		`{"code":-9,"message":"The input params invalid, validation error.","validations":[]}`)

	_, err := gw.Request(context.Background(), testPayment)

	require.Error(t, err)
	assert.True(t, errorx.IsBusinessError(err))
	ce := errorx.GetError(err)
	require.NotNil(t, ce)
	assert.Equal(t, "-9", ce.Params["gatewayCode"])
}

func TestRequest_ErrorEnvelopeAsArray(t *testing.T) {
	gw := errorServer(t, http.StatusBadRequest, `[{"code":-11,"message":"Merchant is not active"}]`)

	_, err := gw.Request(context.Background(), testPayment)

	ce := errorx.GetError(err)
	require.NotNil(t, ce)
	assert.Equal(t, "-11", ce.Params["gatewayCode"])
}

func TestVerify_PaymentFailedIsDecline(t *testing.T) {
	gw := errorServer(t, http.StatusUnprocessableEntity, `{"code":-51,"message":"Session is not valid, session is not active paid try."}`)

	_, err := gw.Verify(context.Background(), testPayment, map[string]string{"Status": "OK", "Authority": "A1"})

	assert.ErrorIs(t, err, payjet.ErrCancelled)
}

func TestVerify_AmountMismatchIsRejection(t *testing.T) {
	gw := errorServer(t, http.StatusUnprocessableEntity, `{"code":-50,"message":"Session is not valid, amounts values is not the same."}`)

	_, err := gw.Verify(context.Background(), testPayment, map[string]string{"Status": "OK", "Authority": "A1"})

	require.Error(t, err)
	assert.True(t, errorx.IsBusinessError(err))
	assert.NotErrorIs(t, err, payjet.ErrCancelled)
}

func TestVerify_TokenMismatch(t *testing.T) {
	gw := newGateway(t, &zarinpalMock{verifyCode: 100, verifyRefID: 1})
	p := *testPayment
	p.Token = "A-issued-for-this-payment"

	_, err := gw.Verify(context.Background(), &p, map[string]string{"Status": "OK", "Authority": "A-other"})

	assert.ErrorIs(t, err, payjet.ErrTokenMismatch)
}

func TestVerify_CaseInsensitiveCallback(t *testing.T) {
	gw := newGateway(t, &zarinpalMock{verifyCode: 100, verifyRefID: 42})
	params := map[string]string{"status": "ok", "authority": "A1"}

	assert.Equal(t, "A1", gw.CallbackOrderID(params))
	res, err := gw.Verify(context.Background(), testPayment, params)
	require.NoError(t, err)
	assert.Equal(t, "42", res.RefID)
}
