package payjet_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/hatami57/microjet/core/errorx"
	"github.com/majid/payjet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaymentValidate(t *testing.T) {
	valid := &payjet.Payment{Amount: 1000, OrderID: "o1", CallbackURL: "https://x/cb"}
	require.NoError(t, valid.Validate())

	assert.Error(t, (*payjet.Payment)(nil).Validate())
	assert.Error(t, (&payjet.Payment{OrderID: "o1", CallbackURL: "x"}).Validate())             // amount
	assert.Error(t, (&payjet.Payment{Amount: 1, CallbackURL: "x"}).Validate())                 // order id
	assert.Error(t, (&payjet.Payment{Amount: 1, OrderID: "o1"}).Validate())                    // callback
	assert.Error(t, (&payjet.Payment{Amount: -5, OrderID: "o1", CallbackURL: "x"}).Validate()) // negative
}

func TestParseCallback_MergesQueryAndForm(t *testing.T) {
	body := url.Values{"RefNum": {"r-1"}, "Status": {"2"}}.Encode()
	req := httptest.NewRequest(http.MethodPost, "/cb?ResNum=o-1&Status=ignored", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	params := payjet.ParseCallback(req)
	assert.Equal(t, "o-1", params["ResNum"]) // from query
	assert.Equal(t, "r-1", params["RefNum"]) // from form
	assert.Equal(t, "2", params["Status"])   // form overrides query
}

func TestRequestResult_Redirect_GET(t *testing.T) {
	res := &payjet.RequestResult{Method: payjet.MethodGET, PaymentURL: "https://bank/pay?t=1"}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/checkout", nil)

	require.NoError(t, res.Redirect(rec, req))
	assert.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "https://bank/pay?t=1", rec.Header().Get("Location"))
}

func TestRequestResult_Redirect_POST_EscapesFields(t *testing.T) {
	res := &payjet.RequestResult{
		Method:     payjet.MethodPOST,
		PaymentURL: "https://bank/pay",
		Params:     map[string]string{"Token": `a"><script>`},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/checkout", nil)

	require.NoError(t, res.Redirect(rec, req))
	html := rec.Body.String()
	assert.Contains(t, html, `action="https://bank/pay"`)
	assert.Contains(t, html, "document.forms[0].submit()")
	// The injected value must be HTML-escaped, not emitted raw.
	assert.NotContains(t, html, "<script>")
}

func TestError_UnwrapsSentinel(t *testing.T) {
	err := payjet.Declined("zarinpal", "verify", "-1", "")
	// errors.Is still matches the sentinel (carried as the errorx.Error Inner)...
	assert.True(t, errors.Is(err, payjet.ErrCancelled))
	// ...and the error now carries a microjet category that maps to HTTP 409.
	assert.True(t, errorx.IsBusinessError(err))
	assert.Contains(t, err.Error(), "zarinpal")
	assert.Contains(t, err.Error(), "-1")
}

func TestParam_IgnoresCase(t *testing.T) {
	params := map[string]string{"Token": "t1", "status": "0"}

	assert.Equal(t, "t1", payjet.Param(params, "Token"))
	assert.Equal(t, "t1", payjet.Param(params, "token"))
	assert.Equal(t, "0", payjet.Param(params, "Status"))
	assert.Equal(t, "", payjet.Param(params, "missing"))
}

func TestParam_PrefersExactMatch(t *testing.T) {
	params := map[string]string{"token": "lower", "Token": "upper"}

	assert.Equal(t, "upper", payjet.Param(params, "Token"))
	assert.Equal(t, "lower", payjet.Param(params, "token"))
}
