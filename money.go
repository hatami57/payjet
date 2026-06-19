package payjet

import (
	"github.com/hatami57/microjet/core/errorx"
	"github.com/hatami57/microjet/core/types/money"
)

// CurrencyIRR is the ISO 4217 code for the Iranian Rial. payjet works in Rials
// everywhere; the money.Money bridge below tags amounts with this currency so
// applications already using microjet's money type can move values in and out
// of payjet without losing currency information. The Rial is a zero-decimal
// currency in microjet's registry, so its minor unit is the Rial itself.
const CurrencyIRR money.CurrencyCode = "IRR"

// RialMoney wraps an integer Rial amount as a currency-tagged money.Money.
// Use it when handing a payjet amount to code that speaks microjet's money type.
func RialMoney(rials int64) money.Money {
	return money.FromMinorUnits(rials, CurrencyIRR)
}

// ToRials converts a money.Money back to an integer Rial amount. It returns a
// BadRequest error if the currency is not IRR or the value has a fractional
// part (Rials have no sub-unit), so callers fail fast on misconfigured amounts.
func ToRials(m money.Money) (int64, error) {
	if m.CurrencyCode != CurrencyIRR {
		return 0, errorx.NewBadRequestError("amount", "currency must be IRR").
			WithParams("currencyCode", string(m.CurrencyCode))
	}
	if !m.Value.Equal(m.Value.Truncate(0)) {
		return 0, errorx.NewBadRequestError("amount", "Rial amount must be a whole number").
			WithParams("value", m.Value.String())
	}
	return m.MinorUnits(), nil
}

// Money returns the payment amount as a currency-tagged money.Money (IRR).
func (p *Payment) Money() money.Money { return RialMoney(p.Amount) }

// Money returns the verified amount as a currency-tagged money.Money (IRR).
func (r *VerifyResult) Money() money.Money { return RialMoney(r.Amount) }
