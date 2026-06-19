// Example application demonstrating payjet built on the microjet stack.
//
// It uses microjet's host orchestrator (config, logging, graceful shutdown),
// a PostgreSQL-backed payment store via gormx.Table[T], and the gin router
// microjet's HTTP server already wires with logging/recovery/health middleware.
//
// Configure the gateway and credentials in config.toml under [payjet],
// or override anything via env vars (APP_ prefix). The default gateway is the
// virtual one, so the app runs end-to-end with no real bank — but it does need
// a PostgreSQL database matching the [database] section.
//
//	# start postgres (matching config.toml), then:
//	go run ./example
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/hatami57/microjet/core/configx"
	"github.com/hatami57/microjet/core/errorx"
	"github.com/hatami57/microjet/gormx/postgres"
	"github.com/hatami57/microjet/host"
	"github.com/majid/payjet"
	"github.com/majid/payjet/idpay"
	"github.com/majid/payjet/mellat"
	"github.com/majid/payjet/parsian"
	"github.com/majid/payjet/saman"
	"github.com/majid/payjet/virtual"
	"github.com/majid/payjet/zarinpal"
)

// ── persistence ──────────────────────────────────────────────────────────────
//
// This example does not hand-roll any storage: payjet.Module() registers the
// default gormx-backed PaymentStore and TransactionStore, which persist into the
// host's database (the [database] section). Handlers resolve those interfaces
// from the service container and use them directly.

// loadPayment finds a stored payment by either its OrderID or its gateway Token,
// covering gateways that echo the order ID and ones that echo only a token
// (Zarinpal), as reported by Gateway.CallbackOrderID.
func loadPayment(ctx context.Context, ps payjet.PaymentStore, key string) (*payjet.StoredPayment, error) {
	rec, err := ps.GetPayment(ctx, key)
	if err != nil || rec != nil {
		return rec, err
	}
	return ps.GetPaymentByToken(ctx, key)
}

// ── gateway configuration (read from [payjet]) ───────────────────────────────

type payjetConfig struct {
	Gateway  string `mapstructure:"gateway"`
	Zarinpal struct {
		MerchantID string `mapstructure:"merchantID"`
		Sandbox    bool   `mapstructure:"sandbox"`
	} `mapstructure:"zarinpal"`
	IDPay struct {
		APIKey  string `mapstructure:"apiKey"`
		Sandbox bool   `mapstructure:"sandbox"`
	} `mapstructure:"idpay"`
	Saman struct {
		TerminalID string `mapstructure:"terminalID"`
	} `mapstructure:"saman"`
	Parsian struct {
		LoginAccount string `mapstructure:"loginAccount"`
	} `mapstructure:"parsian"`
	Mellat struct {
		TerminalID int64  `mapstructure:"terminalID"`
		Username   string `mapstructure:"username"`
		Password   string `mapstructure:"password"`
	} `mapstructure:"mellat"`
}

// ReadConfig implements configx.Configurable, populating the gateway selection
// and credentials from the [payjet] section of the config (env-overridable via
// the APP_PAYJET_* prefix). It defaults to the virtual gateway so the app boots
// with no real bank configured.
func (c *payjetConfig) ReadConfig(r configx.Reader) error {
	r.SetDefault("payjet.gateway", "virtual")
	return r.Read("payjet", c)
}

// buildGateway selects and constructs the gateway from config. For the virtual
// gateway it also mounts the local HTML payment page on the router.
func buildGateway(cfg payjetConfig, baseURL string, r *gin.Engine) (payjet.Gateway, error) {
	switch cfg.Gateway {
	case "zarinpal":
		if cfg.Zarinpal.MerchantID == "" {
			return nil, errorx.NewBadRequestError("config", "zarinpal.merchantID is required")
		}
		var opts []zarinpal.Option
		if cfg.Zarinpal.Sandbox {
			opts = append(opts, zarinpal.WithSandbox())
		}
		return zarinpal.New(cfg.Zarinpal.MerchantID, opts...), nil

	case "idpay":
		if cfg.IDPay.APIKey == "" {
			return nil, errorx.NewBadRequestError("config", "idpay.apiKey is required")
		}
		var opts []idpay.Option
		if cfg.IDPay.Sandbox {
			opts = append(opts, idpay.WithSandbox())
		}
		return idpay.New(cfg.IDPay.APIKey, opts...), nil

	case "saman":
		if cfg.Saman.TerminalID == "" {
			return nil, errorx.NewBadRequestError("config", "saman.terminalID is required")
		}
		return saman.New(cfg.Saman.TerminalID), nil

	case "parsian":
		if cfg.Parsian.LoginAccount == "" {
			return nil, errorx.NewBadRequestError("config", "parsian.loginAccount is required")
		}
		return parsian.New(cfg.Parsian.LoginAccount), nil

	case "mellat":
		if cfg.Mellat.TerminalID == 0 {
			return nil, errorx.NewBadRequestError("config", "mellat.terminalID is required")
		}
		return mellat.New(mellat.Config{
			TerminalID: cfg.Mellat.TerminalID,
			Username:   cfg.Mellat.Username,
			Password:   cfg.Mellat.Password,
		}), nil

	default: // virtual
		vgw := virtual.New(baseURL + "/virtual-pay")
		r.GET("/virtual-pay", gin.WrapH(vgw.Handler()))
		r.POST("/virtual-pay", gin.WrapH(vgw.Handler()))
		return vgw, nil
	}
}

// ── route registration & handlers ────────────────────────────────────────────

func registerRoutes(cfg *payjetConfig) host.HandlerFunc {
	return func(app *host.App) error {
		// By route-registration time the HTTP server's config has been read, so
		// Addr() reflects the configured host:port.
		baseURL := "http://" + app.HTTPServer.Addr()
		r := app.HTTPServer.Router

		gw, err := buildGateway(*cfg, baseURL, r)
		if err != nil {
			return err
		}
		app.Logger.Info("payjet gateway ready", "gateway", cfg.Gateway)

		// The default stores registered by payjet.Module() are resolved from the
		// service container — no storage code to write or migrations to run here.
		ps := host.MustResolveService[payjet.PaymentStore](app)
		ts := host.MustResolveService[payjet.TransactionStore](app)

		r.GET("/", indexHandler)
		r.POST("/checkout", checkoutHandler(app, gw, ps, cfg.Gateway, baseURL))
		r.GET("/payment/callback", callbackHandler(app, gw, ps, ts, cfg.Gateway))
		r.POST("/payment/callback", callbackHandler(app, gw, ps, ts, cfg.Gateway))
		r.GET("/payment/result", resultHandler)
		return nil
	}
}

// POST /checkout — creates a payment, persists it, and redirects to the gateway.
func checkoutHandler(app *host.App, gw payjet.Gateway, ps payjet.PaymentStore, gateway, baseURL string) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		orderID := fmt.Sprintf("order-%d", time.Now().UnixMilli())

		p := &payjet.Payment{
			OrderID:     orderID,
			Amount:      500_000, // 50,000 Tomans in Rials
			CallbackURL: baseURL + "/payment/callback",
			Description: "خرید از فروشگاه نمونه",
		}
		// payjet's money bridge tags the amount with its currency for logging.
		app.Logger.Info("checkout started",
			"orderID", orderID,
			"amount", p.Money().Value.String(),
			"currency", p.Money().CurrencyCode,
		)

		sp := payjet.NewStoredPayment(gateway, p)
		if err := ps.SavePayment(ctx, sp); err != nil {
			app.Logger.Error("save payment failed", "orderID", orderID, "error", err)
			c.Redirect(http.StatusFound, "/payment/result?status=error&msg=internal+error")
			return
		}

		res, err := gw.Request(ctx, p)
		if err != nil {
			app.Logger.Error("gateway request failed", "orderID", orderID, "error", err)
			c.Redirect(http.StatusFound, "/payment/result?status=error&msg=gateway+error")
			return
		}

		// Record the token so a token-only callback (Zarinpal) can be matched.
		if res.Token != "" {
			sp.Token = res.Token
			_ = ps.SavePayment(ctx, sp)
		}

		// One call handles both GET redirects and POST self-submitting forms.
		if err := res.Redirect(c.Writer, c.Request); err != nil {
			app.Logger.Error("redirect failed", "orderID", orderID, "error", err)
			c.Redirect(http.StatusFound, "/payment/result?status=error&msg=redirect+error")
		}
	}
}

// GET|POST /payment/callback — the bank returns the user here after payment.
func callbackHandler(app *host.App, gw payjet.Gateway, ps payjet.PaymentStore, ts payjet.TransactionStore, gateway string) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		params := payjet.ParseCallback(c.Request)
		key := gw.CallbackOrderID(params)

		rec, err := loadPayment(ctx, ps, key)
		if err != nil {
			app.Logger.Error("load payment failed", "key", key, "error", err)
			c.Redirect(http.StatusFound, "/payment/result?status=error&msg=internal+error")
			return
		}
		if rec == nil {
			c.Redirect(http.StatusFound, "/payment/result?status=error&msg=order+not+found")
			return
		}

		p := &payjet.Payment{OrderID: rec.OrderID, Amount: rec.Amount, CallbackURL: rec.CallbackURL}
		result, err := gw.Verify(ctx, p, params)
		if err != nil {
			_ = ps.SetStatus(ctx, rec.OrderID, payjet.StatusFailed)
			// A user cancelling is a normal flow, not a system failure.
			if errors.Is(err, payjet.ErrCancelled) {
				c.Redirect(http.StatusFound, "/payment/result?status=failed")
				return
			}
			app.Logger.Error("verify failed", "orderID", rec.OrderID, "error", err)
			c.Redirect(http.StatusFound, "/payment/result?status=failed")
			return
		}

		// Mark the order succeeded and append the verified transaction.
		if err := ps.SetStatus(ctx, rec.OrderID, payjet.StatusSucceeded); err != nil {
			app.Logger.Error("update payment failed", "orderID", rec.OrderID, "error", err)
		}
		if err := ts.SaveTransaction(ctx, payjet.NewTransaction(gateway, result)); err != nil {
			app.Logger.Error("save transaction failed", "orderID", rec.OrderID, "error", err)
		}

		app.Logger.Info("payment succeeded",
			"orderID", result.OrderID, "refID", result.RefID, "card", result.CardNumber)
		c.Redirect(http.StatusFound,
			fmt.Sprintf("/payment/result?status=success&ref=%s", result.RefID))
	}
}

// ── main ─────────────────────────────────────────────────────────────────────

func main() {
	cfg := &payjetConfig{}

	host.MustNew().
		Configure(cfg). // read [payjet] into cfg before services start
		WithDatabase(postgres.Driver()).
		WithModule(payjet.Module()). // registers + migrates the default stores
		WithHTTPServer(registerRoutes(cfg)).
		MustRun() // inits services, starts HTTP, blocks until signal, then shuts down
}

// ── static HTML pages ──────────────────────────────────────────────────────────

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

// GET /payment/result
func resultHandler(c *gin.Context) {
	status := c.Query("status")
	ref := c.Query("ref")
	msg := c.Query("msg")

	var inner string
	switch status {
	case "success":
		inner = fmt.Sprintf(`<div style="color:green"><h2>✓ پرداخت موفق</h2><p>کد پیگیری: <b>%s</b></p></div>`, ref)
	case "failed":
		inner = `<div style="color:red"><h2>✗ پرداخت ناموفق</h2><p>لطفاً دوباره تلاش کنید.</p></div>`
	default:
		inner = fmt.Sprintf(`<div style="color:orange"><h2>خطا</h2><p>%s</p></div>`, msg)
	}

	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(`<!DOCTYPE html>
<html lang="fa" dir="rtl">
<head><meta charset="UTF-8"><title>نتیجه پرداخت</title>
<style>body{font-family:Tahoma,sans-serif;display:flex;align-items:center;justify-content:center;min-height:100vh;background:#f0f2f5}
.box{background:#fff;padding:2rem;border-radius:10px;box-shadow:0 4px 16px rgba(0,0,0,.1);text-align:center;width:320px}
a{display:inline-block;margin-top:1.5rem;color:#007bff}</style>
</head>
<body><div class="box">`+inner+`<br><a href="/">بازگشت به فروشگاه</a></div></body></html>`))
}
