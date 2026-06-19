package payjet

import (
	"context"
	"time"
)

// PaymentStatus is the lifecycle state of a stored payment.
type PaymentStatus string

const (
	// StatusPending is a payment that has been initiated but not yet verified.
	StatusPending PaymentStatus = "pending"
	// StatusSucceeded is a payment whose Gateway.Verify confirmed it.
	StatusSucceeded PaymentStatus = "succeeded"
	// StatusFailed is a payment the gateway declined, the user cancelled, or
	// that failed verification.
	StatusFailed PaymentStatus = "failed"
)

// StoredPayment is the persisted record of a payment/order: the merchant intent
// carried by Payment plus the gateway it was routed to, the gateway token issued
// at Request time, and its current status. It is keyed by the merchant OrderID.
//
// The struct carries GORM tags so the default gormx-backed store can persist it
// with no extra mapping, but those tags are inert for any other PaymentStore
// implementation.
type StoredPayment struct {
	OrderID     string        `gorm:"primaryKey"`
	Gateway     string        `gorm:"index"`
	Amount      int64         // in Rials
	Status      PaymentStatus `gorm:"index"`
	Token       string        `gorm:"index"` // gateway token from RequestResult, when issued
	CallbackURL string
	Description string
	Mobile      string
	Email       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// TableName is the table the default store persists payments into.
func (StoredPayment) TableName() string { return "payjet_payments" }

// NewStoredPayment builds a pending StoredPayment for the given gateway from a
// Payment. Set Token afterwards (from RequestResult) before saving if the
// gateway issues one.
func NewStoredPayment(gateway string, p *Payment) *StoredPayment {
	return &StoredPayment{
		OrderID:     p.OrderID,
		Gateway:     gateway,
		Amount:      p.Amount,
		Status:      StatusPending,
		CallbackURL: p.CallbackURL,
		Description: p.Description,
		Mobile:      p.Mobile,
		Email:       p.Email,
	}
}

// Transaction is the persisted outcome of a Gateway.Verify: the gateway's
// reference/trace number, the masked card number, the verified amount, and the
// raw callback params kept for auditing and reconciliation. Several transactions
// may exist for one OrderID (e.g. a retried verification).
type Transaction struct {
	ID         uint   `gorm:"primaryKey"`
	OrderID    string `gorm:"index"`
	Gateway    string `gorm:"index"`
	Status     PaymentStatus
	RefID      string `gorm:"index"`
	CardNumber string
	Amount     int64             // in Rials
	RawParams  map[string]string `gorm:"serializer:json"`
	CreatedAt  time.Time
}

// TableName is the table the default store persists transactions into.
func (Transaction) TableName() string { return "payjet_transactions" }

// NewTransaction builds a succeeded Transaction for the given gateway from a
// VerifyResult.
func NewTransaction(gateway string, vr *VerifyResult) *Transaction {
	return &Transaction{
		OrderID:    vr.OrderID,
		Gateway:    gateway,
		Status:     StatusSucceeded,
		RefID:      vr.RefID,
		CardNumber: vr.CardNumber,
		Amount:     vr.Amount,
		RawParams:  vr.RawParams,
	}
}

// PaymentStore persists payments/orders. Implement it to back payjet with any
// storage you like; the default gormx-backed implementation (NewDBPaymentStore,
// wired automatically by Module) keeps payments in the host's database so you
// need to implement nothing when running on microjet's default stack.
type PaymentStore interface {
	// SavePayment inserts the payment, or updates it on OrderID conflict.
	SavePayment(ctx context.Context, p *StoredPayment) error
	// GetPayment returns the payment with the given OrderID, or nil if none.
	GetPayment(ctx context.Context, orderID string) (*StoredPayment, error)
	// GetPaymentByToken returns the payment carrying the given gateway Token, or
	// nil if none. Use it for gateways whose callback echoes only a token rather
	// than the merchant OrderID (e.g. Zarinpal).
	GetPaymentByToken(ctx context.Context, token string) (*StoredPayment, error)
	// SetStatus updates the status of the payment with the given OrderID.
	SetStatus(ctx context.Context, orderID string, status PaymentStatus) error
}

// TransactionStore persists verification outcomes. Like PaymentStore it can be
// implemented over any backend; the default gormx-backed implementation
// (NewDBTransactionStore, wired by Module) keeps transactions in the host's
// database.
type TransactionStore interface {
	// SaveTransaction appends a transaction record.
	SaveTransaction(ctx context.Context, t *Transaction) error
	// GetTransaction returns the most recent transaction for the OrderID, or nil
	// if none exists.
	GetTransaction(ctx context.Context, orderID string) (*Transaction, error)
	// ListTransactions returns every transaction for the OrderID, newest first.
	ListTransactions(ctx context.Context, orderID string) ([]*Transaction, error)
}
