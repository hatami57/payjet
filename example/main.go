// Example application demonstrating payjet with Gin.
//
// Run in development (virtual gateway, no real bank):
//
//	go run ./example
//
// Run against a real gateway:
//
//	GATEWAY=zarinpal ZARINPAL_MERCHANT_ID=xxxx-xxxx go run ./example
//	GATEWAY=mellat   MELLAT_TERMINAL_ID=1234 MELLAT_USER=u MELLAT_PASS=p go run ./example
//	GATEWAY=idpay    IDPAY_API_KEY=xxxx go run ./example
//	GATEWAY=saman    SAMAN_TERMINAL_ID=12345678 go run ./example
//	GATEWAY=parsian  PARSIAN_LOGIN=xxxx go run ./example
package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/majid/payjet"
	"github.com/majid/payjet/idpay"
	"github.com/majid/payjet/mellat"
	"github.com/majid/payjet/parsian"
	"github.com/majid/payjet/saman"
	"github.com/majid/payjet/virtual"
	"github.com/majid/payjet/zarinpal"
)

// ── in-memory payment store (replace with DB in production) ──────────────────

type storedPayment struct {
	payment   *payjet.Payment
	createdAt time.Time
}

var (
	paymentsMu sync.RWMutex
	payments   = map[string]*storedPayment{} // orderID → payment
)

func savePayment(p *payjet.Payment) {
	paymentsMu.Lock()
	payments[p.OrderID] = &storedPayment{payment: p, createdAt: time.Now()}
	paymentsMu.Unlock()
}

// savePaymentByToken additionally indexes a payment under the gateway token, so
// gateways that do not echo the order ID in their callback (Zarinpal) can still
// be looked up via Gateway.CallbackOrderID.
func savePaymentByToken(token string, p *payjet.Payment) {
	if token == "" {
		return
	}
	paymentsMu.Lock()
	payments[token] = &storedPayment{payment: p, createdAt: time.Now()}
	paymentsMu.Unlock()
}

func loadPayment(key string) (*payjet.Payment, bool) {
	paymentsMu.RLock()
	defer paymentsMu.RUnlock()
	s, ok := payments[key]
	if !ok {
		return nil, false
	}
	return s.payment, true
}

// ── gateway factory ───────────────────────────────────────────────────────────

func buildGateway(r *gin.Engine) (payjet.Gateway, error) {
	switch os.Getenv("GATEWAY") {

	case "zarinpal":
		mid := os.Getenv("ZARINPAL_MERCHANT_ID")
		if mid == "" {
			return nil, fmt.Errorf("ZARINPAL_MERCHANT_ID is required")
		}
		opts := []zarinpal.Option{}
		if os.Getenv("ZARINPAL_SANDBOX") == "1" {
			opts = append(opts, zarinpal.WithSandbox())
		}
		return zarinpal.New(mid, opts...), nil

	case "mellat":
		tid, err := strconv.ParseInt(os.Getenv("MELLAT_TERMINAL_ID"), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("MELLAT_TERMINAL_ID must be numeric: %w", err)
		}
		opts := []mellat.Option{}
		if os.Getenv("MELLAT_ENGLISH") == "1" {
			opts = append(opts, mellat.WithEnglishPage())
		}
		return mellat.New(mellat.Config{
			TerminalID: tid,
			Username:   os.Getenv("MELLAT_USER"),
			Password:   os.Getenv("MELLAT_PASS"),
		}, opts...), nil

	case "idpay":
		key := os.Getenv("IDPAY_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("IDPAY_API_KEY is required")
		}
		opts := []idpay.Option{}
		if os.Getenv("IDPAY_SANDBOX") == "1" {
			opts = append(opts, idpay.WithSandbox())
		}
		return idpay.New(key, opts...), nil

	case "saman":
		tid := os.Getenv("SAMAN_TERMINAL_ID")
		if tid == "" {
			return nil, fmt.Errorf("SAMAN_TERMINAL_ID is required")
		}
		return saman.New(tid), nil

	case "parsian":
		login := os.Getenv("PARSIAN_LOGIN")
		if login == "" {
			return nil, fmt.Errorf("PARSIAN_LOGIN is required")
		}
		return parsian.New(login), nil

	default:
		// Development: virtual gateway — no bank needed.
		log.Println("[payjet] using virtual gateway (development mode)")
		vgw := virtual.New("http://localhost:8080/virtual-pay")
		// Mount the HTML payment page at the same path used in New().
		r.GET("/virtual-pay", gin.WrapH(vgw.Handler()))
		r.POST("/virtual-pay", gin.WrapH(vgw.Handler()))
		return vgw, nil
	}
}

// ── handlers ─────────────────────────────────────────────────────────────────

// GET /
func indexHandler(c *gin.Context) {
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(`<!DOCTYPE html>
<html lang="fa" dir="rtl">
<head><meta charset="UTF-8"><title>فروشگاه نمونه</title>
<style>
  body{font-family:Tahoma,sans-serif;display:flex;align-items:center;justify-content:center;min-height:100vh;background:#f0f2f5}
  .box{background:#fff;padding:2rem;border-radius:10px;box-shadow:0 4px 16px rgba(0,0,0,.1);text-align:center;width:320px}
  h2{margin-bottom:1.5rem;color:#333}
  p{color:#555;margin-bottom:1.5rem}
  button{background:#28a745;color:#fff;border:none;padding:.75rem 2rem;border-radius:6px;font-size:1rem;cursor:pointer;font-family:inherit}
</style>
</head>
<body>
<div class="box">
  <h2>محصول نمونه</h2>
  <p>قیمت: ۵۰،۰۰۰ تومان</p>
  <form method="POST" action="/checkout">
    <button type="submit">خرید و پرداخت</button>
  </form>
</div>
</body>
</html>`))
}

// POST /checkout — creates a payment and redirects to the gateway
func checkoutHandler(gw payjet.Gateway) gin.HandlerFunc {
	return func(c *gin.Context) {
		orderID := fmt.Sprintf("order-%d", time.Now().UnixMilli())

		p := &payjet.Payment{
			OrderID:     orderID,
			Amount:      500_000, // 50,000 Tomans in Rials
			CallbackURL: "http://localhost:8080/payment/callback",
			Description: "خرید از فروشگاه نمونه",
		}
		savePayment(p)

		res, err := gw.Request(c.Request.Context(), p)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		// Index by token too, so gateways that don't echo the order ID in their
		// callback (Zarinpal) can still be looked up in callbackHandler.
		savePaymentByToken(res.Token, p)

		// One call handles both GET redirects and POST self-submitting forms.
		if err := res.Redirect(c.Writer, c.Request); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
	}
}

// GET /payment/callback — the bank posts/gets here after payment
func callbackHandler(gw payjet.Gateway) gin.HandlerFunc {
	return func(c *gin.Context) {
		// The SDK merges query + form fields; the gateway knows its own ID field.
		params := payjet.ParseCallback(c.Request)
		key := gw.CallbackOrderID(params)

		p, ok := loadPayment(key)
		if !ok {
			c.Redirect(http.StatusFound, "/payment/result?status=error&msg=order+not+found")
			return
		}

		verifyResult, err := gw.Verify(c.Request.Context(), p, params)
		if err != nil {
			// A user cancelling is a normal flow, not a system failure.
			if errors.Is(err, payjet.ErrCancelled) {
				c.Redirect(http.StatusFound, "/payment/result?status=failed")
				return
			}
			log.Printf("[payjet] verify error for order %s: %v", p.OrderID, err)
			c.Redirect(http.StatusFound, "/payment/result?status=failed")
			return
		}

		log.Printf("[payjet] payment succeeded: order=%s refID=%s card=%s",
			verifyResult.OrderID, verifyResult.RefID, verifyResult.CardNumber)

		c.Redirect(http.StatusFound,
			fmt.Sprintf("/payment/result?status=success&ref=%s", verifyResult.RefID))
	}
}

// GET /payment/result
func resultHandler(c *gin.Context) {
	status := c.Query("status")
	ref := c.Query("ref")
	msg := c.Query("msg")

	var html string
	switch status {
	case "success":
		html = fmt.Sprintf(`<div style="color:green"><h2>✓ پرداخت موفق</h2><p>کد پیگیری: <b>%s</b></p></div>`, ref)
	case "failed":
		html = `<div style="color:red"><h2>✗ پرداخت ناموفق</h2><p>لطفاً دوباره تلاش کنید.</p></div>`
	default:
		html = fmt.Sprintf(`<div style="color:orange"><h2>خطا</h2><p>%s</p></div>`, msg)
	}

	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(`<!DOCTYPE html>
<html lang="fa" dir="rtl">
<head><meta charset="UTF-8"><title>نتیجه پرداخت</title>
<style>body{font-family:Tahoma,sans-serif;display:flex;align-items:center;justify-content:center;min-height:100vh;background:#f0f2f5}
.box{background:#fff;padding:2rem;border-radius:10px;box-shadow:0 4px 16px rgba(0,0,0,.1);text-align:center;width:320px}
a{display:inline-block;margin-top:1.5rem;color:#007bff}</style>
</head>
<body><div class="box">`+html+`<br><a href="/">بازگشت به فروشگاه</a></div></body></html>`))
}

// ── main ─────────────────────────────────────────────────────────────────────

func main() {
	r := gin.Default()

	gw, err := buildGateway(r)
	if err != nil {
		log.Fatalf("gateway config error: %v", err)
	}

	r.GET("/", indexHandler)
	r.POST("/checkout", checkoutHandler(gw))
	r.GET("/payment/callback", callbackHandler(gw))
	r.POST("/payment/callback", callbackHandler(gw)) // some gateways POST the callback
	r.GET("/payment/result", resultHandler)

	addr := ":8080"
	log.Printf("[payjet] listening on http://localhost%s", addr)
	log.Fatal(r.Run(addr))
}
