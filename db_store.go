// This file provides the default gormx-backed PaymentStore and TransactionStore.
// They build on microjet's database stack: the shared *gorm.DB (gormx.Of(app)) and the
// generic gormx.Table helpers. Because they reuse the host's connection, payments
// and transactions land in the same database as the rest of the application — no
// second driver, no second handle, and nothing for the user to implement when the
// host already has a database configured.
package payjet

import (
	"context"
	"errors"

	"github.com/hatami57/microjet/gormx"
	"github.com/hatami57/microjet/host"
	"gorm.io/gorm"
)

// ── payments ──────────────────────────────────────────────────────────────────

// dbPaymentStore is the default gormx-backed PaymentStore.
type dbPaymentStore struct {
	gormx.BaseRepository
	db    *gorm.DB
	table *gormx.Table[StoredPayment]
}

// NewDBPaymentStore returns the default PaymentStore. It is a microjet service:
// the host injects the shared *gorm.DB during Init and migrates the
// payjet_payments table during Setup. Module registers it automatically.
func NewDBPaymentStore() PaymentStore { return &dbPaymentStore{} }

func (d *dbPaymentStore) initDB(db *gorm.DB) {
	base := gormx.NewBaseRepository(db)
	d.BaseRepository = base
	d.db = db
	d.table = gormx.NewTableFor[StoredPayment](&base)
}

func (d *dbPaymentStore) Init(app *host.App) error { d.initDB(gormx.Of(app)); return nil }

func (d *dbPaymentStore) Setup(app *host.App) error {
	return gormx.Of(app).AutoMigrate(&StoredPayment{})
}

func (d *dbPaymentStore) SavePayment(ctx context.Context, p *StoredPayment) error {
	return d.table.Upsert(ctx, p)
}

func (d *dbPaymentStore) GetPayment(ctx context.Context, orderID string) (*StoredPayment, error) {
	return d.table.First(ctx, "order_id = ?", orderID)
}

func (d *dbPaymentStore) GetPaymentByToken(ctx context.Context, token string) (*StoredPayment, error) {
	return d.table.First(ctx, "token = ?", token)
}

func (d *dbPaymentStore) SetStatus(ctx context.Context, orderID string, status PaymentStatus) error {
	return d.table.UpdateMap(ctx, map[string]any{"status": status}, "order_id = ?", orderID)
}

// ── transactions ──────────────────────────────────────────────────────────────

// dbTransactionStore is the default gormx-backed TransactionStore.
type dbTransactionStore struct {
	gormx.BaseRepository
	db    *gorm.DB
	table *gormx.Table[Transaction]
}

// NewDBTransactionStore returns the default TransactionStore. Like the payment
// store it is a microjet service that shares the host's database; Module
// registers it automatically.
func NewDBTransactionStore() TransactionStore { return &dbTransactionStore{} }

func (d *dbTransactionStore) initDB(db *gorm.DB) {
	base := gormx.NewBaseRepository(db)
	d.BaseRepository = base
	d.db = db
	d.table = gormx.NewTableFor[Transaction](&base)
}

func (d *dbTransactionStore) Init(app *host.App) error { d.initDB(gormx.Of(app)); return nil }

func (d *dbTransactionStore) Setup(app *host.App) error {
	return gormx.Of(app).AutoMigrate(&Transaction{})
}

func (d *dbTransactionStore) SaveTransaction(ctx context.Context, t *Transaction) error {
	return d.table.Create(ctx, t)
}

func (d *dbTransactionStore) GetTransaction(ctx context.Context, orderID string) (*Transaction, error) {
	var rec Transaction
	err := d.db.WithContext(ctx).Where("order_id = ?", orderID).Order("id DESC").First(&rec).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &rec, nil
}

func (d *dbTransactionStore) ListTransactions(ctx context.Context, orderID string) ([]*Transaction, error) {
	var recs []*Transaction
	err := d.db.WithContext(ctx).Where("order_id = ?", orderID).Order("id DESC").Find(&recs).Error
	return recs, err
}
