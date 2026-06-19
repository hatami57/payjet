package payjet

import (
	"context"
	"log/slog"
	"testing"

	"github.com/hatami57/microjet/gormx"
	"github.com/hatami57/microjet/gormx/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newTestDB opens an in-memory SQLite database using microjet's pure-Go driver,
// the same one a host app would use, so the default stores are exercised against
// a real gorm.DB.
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := sqlite.Driver().Open(gormx.Config{Name: ":memory:"}, slog.Default())
	require.NoError(t, err)
	return db
}

func newTestStores(t *testing.T) (*dbPaymentStore, *dbTransactionStore) {
	t.Helper()
	db := newTestDB(t)
	require.NoError(t, db.AutoMigrate(&StoredPayment{}, &Transaction{}))

	ps := &dbPaymentStore{}
	ps.initDB(db)
	ts := &dbTransactionStore{}
	ts.initDB(db)
	return ps, ts
}

func TestPaymentStore_SaveLoadStatus(t *testing.T) {
	ctx := context.Background()
	ps, _ := newTestStores(t)

	sp := NewStoredPayment("zarinpal", &Payment{
		OrderID:     "order-1",
		Amount:      500_000,
		CallbackURL: "https://shop.example/callback",
		Description: "test",
	})
	sp.Token = "A0000000000000000000000000000012345"
	require.NoError(t, ps.SavePayment(ctx, sp))

	got, err := ps.GetPayment(ctx, "order-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "zarinpal", got.Gateway)
	assert.Equal(t, int64(500_000), got.Amount)
	assert.Equal(t, StatusPending, got.Status)

	byToken, err := ps.GetPaymentByToken(ctx, sp.Token)
	require.NoError(t, err)
	require.NotNil(t, byToken)
	assert.Equal(t, "order-1", byToken.OrderID)

	require.NoError(t, ps.SetStatus(ctx, "order-1", StatusSucceeded))
	got, err = ps.GetPayment(ctx, "order-1")
	require.NoError(t, err)
	assert.Equal(t, StatusSucceeded, got.Status)
}

func TestPaymentStore_MissingReturnsNil(t *testing.T) {
	ctx := context.Background()
	ps, _ := newTestStores(t)

	got, err := ps.GetPayment(ctx, "nope")
	require.NoError(t, err)
	assert.Nil(t, got)

	got, err = ps.GetPaymentByToken(ctx, "nope")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestPaymentStore_SaveUpserts(t *testing.T) {
	ctx := context.Background()
	ps, _ := newTestStores(t)

	sp := NewStoredPayment("idpay", &Payment{OrderID: "order-2", Amount: 100})
	require.NoError(t, ps.SavePayment(ctx, sp))
	sp.Amount = 200
	require.NoError(t, ps.SavePayment(ctx, sp))

	got, err := ps.GetPayment(ctx, "order-2")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, int64(200), got.Amount)
}

func TestTransactionStore_SaveListGet(t *testing.T) {
	ctx := context.Background()
	_, ts := newTestStores(t)

	first := NewTransaction("mellat", &VerifyResult{
		OrderID:   "order-3",
		RefID:     "ref-1",
		Amount:    500_000,
		RawParams: map[string]string{"ResCode": "0", "RefId": "ref-1"},
	})
	require.NoError(t, ts.SaveTransaction(ctx, first))

	second := NewTransaction("mellat", &VerifyResult{OrderID: "order-3", RefID: "ref-2", Amount: 500_000})
	require.NoError(t, ts.SaveTransaction(ctx, second))

	// GetTransaction returns the newest (highest id) for the order.
	latest, err := ts.GetTransaction(ctx, "order-3")
	require.NoError(t, err)
	require.NotNil(t, latest)
	assert.Equal(t, "ref-2", latest.RefID)

	list, err := ts.ListTransactions(ctx, "order-3")
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, "ref-2", list[0].RefID) // newest first
	assert.Equal(t, "ref-1", list[1].RefID)
	// RawParams round-trips through the JSON serializer column.
	assert.Equal(t, "0", list[1].RawParams["ResCode"])
}

func TestTransactionStore_MissingReturnsNil(t *testing.T) {
	ctx := context.Background()
	_, ts := newTestStores(t)

	got, err := ts.GetTransaction(ctx, "nope")
	require.NoError(t, err)
	assert.Nil(t, got)

	list, err := ts.ListTransactions(ctx, "nope")
	require.NoError(t, err)
	assert.Empty(t, list)
}

// Verify the default constructors satisfy the public interfaces.
var (
	_ PaymentStore     = NewDBPaymentStore()
	_ TransactionStore = NewDBTransactionStore()
)
