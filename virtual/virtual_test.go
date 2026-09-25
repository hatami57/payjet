package virtual_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hatami57/microjet/core/errorx"

	"github.com/majid/payjet"
	"github.com/majid/payjet/virtual"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testPayment = &payjet.Payment{
	OrderID:     "order-42",
	Amount:      150_000,
	CallbackURL: "http://app.test/callback",
	Description: "Test purchase",
}

// ── Request ───────────────────────────────────────────────────────────────────

func TestRequest_ReturnsTokenAndURL(t *testing.T) {
	gw := virtual.New("http://localhost:8080/pay")
	res, err := gw.Request(context.Background(), testPayment)

	require.NoError(t, err)
	assert.NotEmpty(t, res.Token)
	assert.Equal(t, payjet.MethodGET, res.Method)
	assert.Contains(t, res.PaymentURL, "http://localhost:8080/pay?token=")
	assert.Contains(t, res.PaymentURL, res.Token)
}

func TestRequest_EachCallProducesUniqueToken(t *testing.T) {
	gw := virtual.New("http://localhost:8080/pay")
	r1, _ := gw.Request(context.Background(), testPayment)
	r2, _ := gw.Request(context.Background(), testPayment)
	assert.NotEqual(t, r1.Token, r2.Token)
}

// ── SimulatePayment ───────────────────────────────────────────────────────────

func TestSimulatePayment_Succeed(t *testing.T) {
	gw := virtual.New("http://localhost:8080/pay")
	params := gw.SimulatePayment(testPayment.OrderID, true)

	assert.Equal(t, "true", params["result"])
	assert.Equal(t, testPayment.OrderID, params["OrderID"])
	assert.NotEmpty(t, params["TransactionCode"])
}

func TestSimulatePayment_Cancel(t *testing.T) {
	gw := virtual.New("http://localhost:8080/pay")
	params := gw.SimulatePayment(testPayment.OrderID, false)

	assert.Equal(t, "false", params["result"])
	assert.Equal(t, testPayment.OrderID, params["OrderID"])
	assert.Empty(t, params["TransactionCode"])
}

// ── Verify ────────────────────────────────────────────────────────────────────

func TestVerify_Success(t *testing.T) {
	gw := virtual.New("http://localhost:8080/pay")
	_, _ = gw.Request(context.Background(), testPayment)

	params := gw.SimulatePayment(testPayment.OrderID, true)
	res, err := gw.Verify(context.Background(), testPayment, params)

	require.NoError(t, err)
	assert.Equal(t, testPayment.OrderID, res.OrderID)
	assert.NotEmpty(t, res.RefID)
	assert.Equal(t, params["TransactionCode"], res.RefID)
}

func TestVerify_Cancelled(t *testing.T) {
	gw := virtual.New("http://localhost:8080/pay")
	params := gw.SimulatePayment(testPayment.OrderID, false)

	_, err := gw.Verify(context.Background(), testPayment, params)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cancelled")
}

// A hand-made result=true callback is not a payment: only codes the gateway
// issued from its page or SimulatePayment verify.
func TestVerify_ForgedParamsRejected(t *testing.T) {
	gw := virtual.New("http://localhost:8080/pay")
	_, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"result":          "true",
		"OrderID":         testPayment.OrderID,
		"TransactionCode": "manual-tx-001",
	})
	require.Error(t, err)
	assert.True(t, errorx.IsBusinessError(err))
}

func TestVerify_CodeForAnotherOrderRejected(t *testing.T) {
	gw := virtual.New("http://localhost:8080/pay")
	params := gw.SimulatePayment("some-other-order", true)
	params["OrderID"] = testPayment.OrderID

	_, err := gw.Verify(context.Background(), testPayment, params)
	require.Error(t, err)
}

func TestVerify_ReplayIsAlreadyVerified(t *testing.T) {
	gw := virtual.New("http://localhost:8080/pay")
	params := gw.SimulatePayment(testPayment.OrderID, true)

	first, err := gw.Verify(context.Background(), testPayment, params)
	require.NoError(t, err)
	assert.False(t, first.AlreadyVerified)

	again, err := gw.Verify(context.Background(), testPayment, params)
	require.NoError(t, err)
	assert.True(t, again.AlreadyVerified)
}

func TestVerify_TokenChecked(t *testing.T) {
	gw := virtual.New("http://localhost:8080/pay")
	req, err := gw.Request(context.Background(), testPayment)
	require.NoError(t, err)
	params := gw.SimulatePayment(testPayment.OrderID, true)
	assert.Equal(t, req.Token, params["token"])

	p := *testPayment
	p.Token = req.Token
	_, err = gw.Verify(context.Background(), &p, params)
	require.NoError(t, err)

	p.Token = "not-this-payments-token"
	_, err = gw.Verify(context.Background(), &p, params)
	assert.ErrorIs(t, err, payjet.ErrTokenMismatch)
}

// ── Automated round-trip (no browser) ─────────────────────────────────────────

func TestFullRoundTrip_Automated(t *testing.T) {
	gw := virtual.New("http://localhost:8080/pay")

	// 1. initiate
	reqRes, err := gw.Request(context.Background(), testPayment)
	require.NoError(t, err)

	// 2. simulate click — no HTTP involved
	params := gw.SimulatePayment(testPayment.OrderID, true)

	// 3. verify
	verRes, err := gw.Verify(context.Background(), testPayment, params)
	require.NoError(t, err)
	assert.Equal(t, testPayment.OrderID, verRes.OrderID)
	assert.NotEmpty(t, verRes.RefID)
	_ = reqRes
}

// ── HTTP Handler ──────────────────────────────────────────────────────────────

func TestHandler_GET_RendersPaymentPage(t *testing.T) {
	gw := virtual.New("http://localhost:8080/pay")
	req, _ := gw.Request(context.Background(), testPayment)

	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "?token=" + req.Token)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), testPayment.OrderID)
	assert.Contains(t, string(body), "درگاه مجازی")
	assert.Contains(t, string(body), req.Token)
}

func TestHandler_GET_UnknownToken_Returns400(t *testing.T) {
	gw := virtual.New("http://localhost:8080/pay")
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "?token=no-such-token")
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestHandler_POST_Pay_RedirectsWithSuccess(t *testing.T) {
	gw := virtual.New("http://localhost:8080/pay")
	req, _ := gw.Request(context.Background(), testPayment)

	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()

	client := noRedirectClient()
	resp, err := client.PostForm(srv.URL+"?token="+req.Token,
		url.Values{"token": {req.Token}, "pay": {"1"}})
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusFound, resp.StatusCode)

	loc, err := resp.Location()
	require.NoError(t, err)
	assert.Equal(t, "true", loc.Query().Get("result"))
	assert.NotEmpty(t, loc.Query().Get("TransactionCode"))
	assert.Equal(t, testPayment.OrderID, loc.Query().Get("OrderID"))
	assert.True(t, strings.HasPrefix(loc.String(), testPayment.CallbackURL))

	// The callback the page redirects to verifies against the stored payment.
	params := map[string]string{}
	for k := range loc.Query() {
		params[k] = loc.Query().Get(k)
	}
	p := *testPayment
	p.Token = req.Token
	res, err := gw.Verify(context.Background(), &p, params)
	require.NoError(t, err)
	assert.Equal(t, loc.Query().Get("TransactionCode"), res.RefID)
}

func TestHandler_POST_Cancel_RedirectsWithFailure(t *testing.T) {
	gw := virtual.New("http://localhost:8080/pay")
	req, _ := gw.Request(context.Background(), testPayment)

	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()

	client := noRedirectClient()
	resp, err := client.PostForm(srv.URL+"?token="+req.Token,
		url.Values{"token": {req.Token}, "pay": {"0"}})
	require.NoError(t, err)

	loc, _ := resp.Location()
	assert.Equal(t, "false", loc.Query().Get("result"))
	assert.Empty(t, loc.Query().Get("TransactionCode"))
}

func TestHandler_POST_TokenConsumedAfterPay(t *testing.T) {
	gw := virtual.New("http://localhost:8080/pay")
	req, _ := gw.Request(context.Background(), testPayment)

	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()

	client := noRedirectClient()
	// first POST — should succeed
	resp1, _ := client.PostForm(srv.URL+"?token="+req.Token,
		url.Values{"token": {req.Token}, "pay": {"1"}})
	assert.Equal(t, http.StatusFound, resp1.StatusCode)

	// second POST with same token — token is gone
	resp2, _ := client.PostForm(srv.URL+"?token="+req.Token,
		url.Values{"token": {req.Token}, "pay": {"1"}})
	assert.Equal(t, http.StatusBadRequest, resp2.StatusCode)
}

func TestHandler_CallbackURL_WithExistingQuery(t *testing.T) {
	p := &payjet.Payment{
		OrderID:     "x1",
		Amount:      1000,
		CallbackURL: "http://app.test/cb?lang=fa",
	}
	gw := virtual.New("http://localhost:8080/pay")
	req, _ := gw.Request(context.Background(), p)

	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()

	client := noRedirectClient()
	resp, _ := client.PostForm(srv.URL+"?token="+req.Token,
		url.Values{"token": {req.Token}, "pay": {"1"}})

	loc, _ := resp.Location()
	// existing query params must be preserved, result appended with &
	assert.Equal(t, "fa", loc.Query().Get("lang"))
	assert.Equal(t, "true", loc.Query().Get("result"))
}

// ── helpers ───────────────────────────────────────────────────────────────────

func noRedirectClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// ── Refund ────────────────────────────────────────────────────────────────────

func TestRefund_VerifiedPayment(t *testing.T) {
	gw := virtual.New("http://localhost:8080/pay")
	params := gw.SimulatePayment(testPayment.OrderID, true)
	v, err := gw.Verify(context.Background(), testPayment, params)
	require.NoError(t, err)

	res, err := gw.Refund(context.Background(), testPayment, v)
	require.NoError(t, err)
	assert.False(t, res.AlreadyRefunded)
	assert.Equal(t, v.RefID, res.RefID)

	again, err := gw.Refund(context.Background(), testPayment, v)
	require.NoError(t, err)
	assert.True(t, again.AlreadyRefunded)
}

func TestRefund_UnverifiedPaymentRejected(t *testing.T) {
	gw := virtual.New("http://localhost:8080/pay")
	params := gw.SimulatePayment(testPayment.OrderID, true)

	_, err := gw.Refund(context.Background(), testPayment, &payjet.VerifyResult{RefID: params["TransactionCode"]})

	require.Error(t, err)
	assert.True(t, errorx.IsBusinessError(err))
}

func TestRefund_UnknownCodeRejected(t *testing.T) {
	gw := virtual.New("http://localhost:8080/pay")

	_, err := gw.Refund(context.Background(), testPayment, &payjet.VerifyResult{RefID: "made-up"})

	require.Error(t, err)
}

// ── Housekeeping ──────────────────────────────────────────────────────────────

func TestRetention_ForgetsOldPayments(t *testing.T) {
	gw := virtual.New("http://localhost:8080/pay", virtual.WithRetention(time.Millisecond))
	old, err := gw.Request(context.Background(), testPayment)
	require.NoError(t, err)
	params := gw.SimulatePayment(testPayment.OrderID, true)

	time.Sleep(5 * time.Millisecond)
	_, err = gw.Request(context.Background(), testPayment) // prunes

	// Both the pending page and the paid code are gone.
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "?token=" + old.Token)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	_, err = gw.Verify(context.Background(), testPayment, params)
	assert.Error(t, err)
}

func TestHandler_POST_DoubleSubmitRefused(t *testing.T) {
	gw := virtual.New("http://localhost:8080/pay")
	req, _ := gw.Request(context.Background(), testPayment)
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()
	client := noRedirectClient()

	var codes []int
	for range 2 {
		resp, err := client.PostForm(srv.URL, url.Values{"token": {req.Token}, "pay": {"1"}})
		require.NoError(t, err)
		resp.Body.Close()
		codes = append(codes, resp.StatusCode)
	}
	assert.Equal(t, http.StatusFound, codes[0])
	assert.NotEqual(t, http.StatusFound, codes[1], "the second submit must not issue another code")
}
