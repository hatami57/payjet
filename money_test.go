package payjet_test

import (
	"testing"

	"github.com/hatami57/microjet/core"
	"github.com/hatami57/microjet/types/money"
	"github.com/majid/payjet"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRialMoneyRoundTrip(t *testing.T) {
	m := payjet.RialMoney(500_000)
	assert.Equal(t, payjet.CurrencyIRR, m.CurrencyCode)

	rials, err := payjet.ToRials(m)
	require.NoError(t, err)
	assert.Equal(t, int64(500_000), rials)
}

func TestToRialsRejectsWrongCurrency(t *testing.T) {
	_, err := payjet.ToRials(money.Money{Value: decimal.NewFromInt(100), CurrencyCode: "USD"})
	require.Error(t, err)
	assert.True(t, core.IsBadRequestError(err))
}

func TestToRialsRejectsFraction(t *testing.T) {
	_, err := payjet.ToRials(money.Money{Value: decimal.NewFromFloat(10.5), CurrencyCode: payjet.CurrencyIRR})
	require.Error(t, err)
	assert.True(t, core.IsBadRequestError(err))
}

func TestPaymentAndVerifyMoney(t *testing.T) {
	want := payjet.RialMoney(12_000)

	pm := (&payjet.Payment{Amount: 12_000}).Money()
	assert.True(t, pm.Equal(&want))

	vm := (&payjet.VerifyResult{Amount: 12_000}).Money()
	assert.True(t, vm.Equal(&want))
}
