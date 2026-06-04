package saman_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/majid/payjet"
	"github.com/majid/payjet/saman"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── mock server ───────────────────────────────────────────────────────────────

type samanMock struct {
	tokenStatus  int
	token        string
	tokenErrMsg  string
	verifyCode   int
	verifyDesc   string
	verifyRRN    string
	verifyPan    string
	verifyAmount int64
}

func (m *samanMock) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/onlinepg/onlinepg":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":    m.tokenStatus,
				"token":     m.token,
				"errorCode": 0,
				"errorDesc": m.tokenErrMsg,
			})
		case "/verify":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ResultCode":        m.verifyCode,
				"ResultDescription": m.verifyDesc,
				"TransactionDetail": map[string]interface{}{
					"Rrn":             m.verifyRRN,
					"MaskedPan":       m.verifyPan,
					"AffectiveAmount": m.verifyAmount,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
}

func newGateway(t *testing.T, m *samanMock) *saman.Gateway {
	t.Helper()
	srv := m.server(t)
	t.Cleanup(srv.Close)
	return saman.New("123456789",
		saman.WithEndpoints(srv.URL+"/onlinepg/onlinepg", "https://sep.shaparak.ir/OnlinePG/OnlinePG", srv.URL+"/verify"),
	)
}

var testPayment = &payjet.Payment{
	OrderID:     "order-77",
	Amount:      400_000,
	CallbackURL: "https://shop.ir/callback",
	Mobile:      "09150000000",
}

// ── Request ───────────────────────────────────────────────────────────────────

func TestRequest_Success(t *testing.T) {
	gw := newGateway(t, &samanMock{tokenStatus: 1, token: "sep-token-abc123"})

	res, err := gw.Request(context.Background(), testPayment)

	require.NoError(t, err)
	assert.Equal(t, "sep-token-abc123", res.Token)
	assert.Equal(t, payjet.MethodPOST, res.Method)
	assert.Equal(t, "sep-token-abc123", res.Params["Token"])
	assert.Equal(t, "false", res.Params["GetMethod"])
}

func TestRequest_SendsCorrectBody(t *testing.T) {
	var body map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"status": 1, "token": "t"})
	}))
	defer srv.Close()

	gw := saman.New("TERM001", saman.WithEndpoints(srv.URL, "", ""))
	_, _ = gw.Request(context.Background(), testPayment)

	assert.Equal(t, "token", body["action"])
	assert.Equal(t, "TERM001", body["TerminalId"])
	assert.Equal(t, float64(testPayment.Amount), body["Amount"])
	assert.Equal(t, testPayment.OrderID, body["ResNum"])
	assert.Equal(t, testPayment.CallbackURL, body["RedirectUrl"])
	assert.Equal(t, testPayment.Mobile, body["CellNumber"])
}

func TestRequest_TokenFailed(t *testing.T) {
	gw := newGateway(t, &samanMock{tokenStatus: -1, tokenErrMsg: "invalid terminal"})

	_, err := gw.Request(context.Background(), testPayment)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid terminal")
}

// ── Verify ────────────────────────────────────────────────────────────────────

func TestVerify_Success(t *testing.T) {
	gw := newGateway(t, &samanMock{
		tokenStatus: 1, token: "t",
		verifyCode: 0, verifyRRN: "RRN-001", verifyPan: "6037****5566", verifyAmount: testPayment.Amount,
	})

	res, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"Status": "2",
		"ResNum": testPayment.OrderID,
		"RefNum": "ref-num-001",
	})

	require.NoError(t, err)
	assert.Equal(t, "RRN-001", res.RefID)
	assert.Equal(t, "6037****5566", res.CardNumber)
}

func TestVerify_PaymentFailed(t *testing.T) {
	gw := newGateway(t, &samanMock{})

	_, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"Status": "1", // failed
		"ResNum": testPayment.OrderID,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, payjet.ErrCancelled)
}

func TestVerify_OrderIDMismatch(t *testing.T) {
	gw := newGateway(t, &samanMock{tokenStatus: 1, token: "t"})

	_, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"Status": "2",
		"ResNum": "different-order",
		"RefNum": "ref-001",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, payjet.ErrOrderMismatch)
}

func TestVerify_AmountMismatch(t *testing.T) {
	gw := newGateway(t, &samanMock{
		tokenStatus: 1, token: "t",
		verifyCode: 0, verifyAmount: 999, // wrong amount returned by gateway
	})

	_, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"Status": "2",
		"ResNum": testPayment.OrderID,
		"RefNum": "ref-001",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, payjet.ErrAmountMismatch)
}

func TestVerify_GatewayError(t *testing.T) {
	gw := newGateway(t, &samanMock{
		tokenStatus: 1, token: "t",
		verifyCode: 404, verifyDesc: "transaction not found", verifyAmount: testPayment.Amount,
	})

	_, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"Status": "2",
		"ResNum": testPayment.OrderID,
		"RefNum": "ref-404",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}
