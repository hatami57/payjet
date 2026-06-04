package virtual_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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

func TestVerify_ManualParams(t *testing.T) {
	gw := virtual.New("http://localhost:8080/pay")
	res, err := gw.Verify(context.Background(), testPayment, map[string]string{
		"result":          "true",
		"OrderID":         testPayment.OrderID,
		"TransactionCode": "manual-tx-001",
	})
	require.NoError(t, err)
	assert.Equal(t, "manual-tx-001", res.RefID)
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
