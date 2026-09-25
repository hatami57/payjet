package saman_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/hatami57/microjet/core/errorx"
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
	// verifyRefNum overrides the RefNum the verify response echoes back.
	verifyRefNum string
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
			// Saman echoes the verified transaction's RefNum and terminal.
			var req struct{ RefNum, TerminalNumber string }
			_ = json.NewDecoder(r.Body).Decode(&req)
			refNum := req.RefNum
			if m.verifyRefNum != "" {
				refNum = m.verifyRefNum
			}
			terminal, _ := strconv.ParseInt(req.TerminalNumber, 10, 64)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ResultCode":        m.verifyCode,
				"ResultDescription": m.verifyDesc,
				"TransactionDetail": map[string]interface{}{
					"Rrn":             m.verifyRRN,
					"RefNum":          refNum,
					"MaskedPan":       m.verifyPan,
					"TerminalNumber":  terminal,
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

func TestVerify_TransportFailureIsGatewayFault(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close() // every call fails to connect
	gw := saman.New("123456789", saman.WithEndpoints(srv.URL, "", srv.URL))

	_, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"Status": "2", "ResNum": testPayment.OrderID, "RefNum": "ref-1",
	})

	require.Error(t, err)
	assert.True(t, errorx.IsInternalError(err))
	ce := errorx.GetError(err)
	require.NotNil(t, ce)
	assert.Equal(t, "saman", ce.Subject)
	assert.Equal(t, "verify", ce.Params["op"])
}

func TestVerify_RefNumMismatch(t *testing.T) {
	gw := newGateway(t, &samanMock{verifyCode: 0, verifyRRN: "1", verifyAmount: testPayment.Amount, verifyRefNum: "someone-elses-ref"})

	_, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"Status": "2", "ResNum": testPayment.OrderID, "RefNum": "ref-1",
	})

	require.Error(t, err)
	assert.True(t, errorx.IsInternalError(err))
}

func TestVerify_CaseInsensitiveCallback(t *testing.T) {
	gw := newGateway(t, &samanMock{verifyCode: 0, verifyRRN: "rrn-9", verifyAmount: testPayment.Amount})
	params := map[string]string{"status": "2", "resnum": testPayment.OrderID, "refnum": "ref-1"}

	assert.Equal(t, testPayment.OrderID, gw.CallbackOrderID(params))
	res, err := gw.Verify(context.Background(), testPayment, params)
	require.NoError(t, err)
	assert.Equal(t, "rrn-9", res.RefID)
}

// ── Refund ────────────────────────────────────────────────────────────────────

func reverseServer(t *testing.T, resp map[string]any) (*saman.Gateway, *map[string]any) {
	t.Helper()
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&sent)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return saman.New("123456789", saman.WithReverseURL(srv.URL)), &sent
}

var verified = &payjet.VerifyResult{
	RefID:     "rrn-1",
	OrderID:   "order-77",
	RawParams: map[string]string{"Status": "2", "ResNum": "order-77", "RefNum": "ref-num-001"},
}

func TestRefund_Success(t *testing.T) {
	gw, sent := reverseServer(t, map[string]any{"ResultCode": 0, "ResultDescription": "OK", "Success": true})

	res, err := gw.Refund(context.Background(), testPayment, verified)

	require.NoError(t, err)
	assert.Equal(t, "ref-num-001", res.RefID)
	assert.Equal(t, testPayment.Amount, res.Amount)
	assert.Equal(t, "ref-num-001", (*sent)["RefNum"])
	assert.Equal(t, "123456789", (*sent)["TerminalNumber"])
}

func TestRefund_Rejected(t *testing.T) {
	gw, _ := reverseServer(t, map[string]any{"ResultCode": -2, "ResultDescription": "تراکنش یافت نشد", "Success": false})

	_, err := gw.Refund(context.Background(), testPayment, verified)

	ce := errorx.GetError(err)
	require.NotNil(t, ce)
	assert.Equal(t, "-2", ce.Params["gatewayCode"])
	assert.Equal(t, "refund", ce.Params["op"])
}

func TestRefund_RequiresRefNum(t *testing.T) {
	gw, _ := reverseServer(t, map[string]any{"Success": true})

	_, err := gw.Refund(context.Background(), testPayment, &payjet.VerifyResult{RefID: "rrn-1"})

	assert.True(t, errorx.IsBadRequestError(err))
}

func TestVerify_DuplicateIsAlreadyVerified(t *testing.T) {
	gw := newGateway(t, &samanMock{verifyCode: 2, verifyRRN: "rrn-2", verifyAmount: testPayment.Amount})

	res, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"Status": "2", "ResNum": testPayment.OrderID, "RefNum": "ref-1",
	})

	require.NoError(t, err)
	assert.True(t, res.AlreadyVerified)
	assert.Equal(t, "rrn-2", res.RefID)
}

// A duplicate reply for another amount is still not this order's payment.
func TestVerify_DuplicateStillChecksAmount(t *testing.T) {
	gw := newGateway(t, &samanMock{verifyCode: 2, verifyRRN: "rrn-2", verifyAmount: 1000})

	_, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"Status": "2", "ResNum": testPayment.OrderID, "RefNum": "ref-1",
	})

	assert.ErrorIs(t, err, payjet.ErrAmountMismatch)
}

func TestRequest_StringErrorCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":-1,"errorCode":"5","errorDesc":"invalid terminal"}`))
	}))
	t.Cleanup(srv.Close)
	gw := saman.New("123456789", saman.WithEndpoints(srv.URL, "", srv.URL))

	_, err := gw.Request(context.Background(), testPayment)

	require.Error(t, err)
	assert.True(t, errorx.IsBusinessError(err))
	ce := errorx.GetError(err)
	require.NotNil(t, ce)
	assert.Equal(t, "5", ce.Params["gatewayCode"])
}

func TestRefund_EndpointsOverriddenWithoutReverseURL(t *testing.T) {
	gw := saman.New("123456789", saman.WithEndpoints("http://127.0.0.1:1/t", "", "http://127.0.0.1:1/v"))

	_, err := gw.Refund(context.Background(), testPayment, verified)

	assert.True(t, errorx.IsBadRequestError(err))
}
