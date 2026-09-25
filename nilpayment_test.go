package payjet_test

import (
	"context"
	"testing"

	"github.com/hatami57/microjet/core/errorx"
	"github.com/stretchr/testify/assert"

	"github.com/majid/payjet"
	"github.com/majid/payjet/idpay"
	"github.com/majid/payjet/mellat"
	"github.com/majid/payjet/parsian"
	"github.com/majid/payjet/pasargad"
	"github.com/majid/payjet/saman"
	"github.com/majid/payjet/virtual"
	"github.com/majid/payjet/zarinpal"
)

// A nil Payment is a caller bug: every gateway reports it instead of panicking.
func TestNilPaymentIsBadRequest(t *testing.T) {
	gateways := map[string]payjet.Gateway{
		"zarinpal": zarinpal.New("m"),
		"idpay":    idpay.New("k"),
		"saman":    saman.New("1"),
		"parsian":  parsian.New("a"),
		"mellat":   mellat.New(mellat.Config{TerminalID: 1}),
		"pasargad": pasargad.New(pasargad.Config{BaseURL: "http://127.0.0.1:1/"}),
		"virtual":  virtual.New("http://127.0.0.1:1/pay"),
	}
	ctx := context.Background()
	for name, gw := range gateways {
		t.Run(name, func(t *testing.T) {
			_, err := gw.Verify(ctx, nil, map[string]string{})
			assert.True(t, errorx.IsBadRequestError(err), "Verify: %v", err)
			_, err = gw.Request(ctx, nil)
			assert.True(t, errorx.IsBadRequestError(err), "Request: %v", err)
			if r, ok := gw.(payjet.Refunder); ok {
				_, err := r.Refund(ctx, nil, nil)
				assert.True(t, errorx.IsBadRequestError(err), "Refund: %v", err)
			}
		})
	}
}
