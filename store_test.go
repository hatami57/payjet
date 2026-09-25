package payjet

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hatami57/microjet/core/errorx"
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

// A payment saved before its Request returned has no token yet; an empty
// callback key must not find it.
func TestPaymentStore_EmptyTokenMatchesNothing(t *testing.T) {
	ctx := context.Background()
	ps, _ := newTestStores(t)
	require.NoError(t, ps.SavePayment(ctx, NewStoredPayment("zarinpal", &Payment{
		OrderID: "o-1", Amount: 1000, CallbackURL: "https://x/cb",
	})))

	got, err := ps.GetPaymentByToken(ctx, "")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestStoredPayment_PaymentCarriesToken(t *testing.T) {
	sp := NewStoredPayment("parsian", &Payment{
		OrderID: "o-1", Amount: 1000, CallbackURL: "https://x/cb", Description: "d",
	})
	sp.Token = "tok-1"

	p := sp.Payment()
	assert.Equal(t, "o-1", p.OrderID)
	assert.Equal(t, int64(1000), p.Amount)
	assert.Equal(t, "https://x/cb", p.CallbackURL)
	assert.Equal(t, "tok-1", p.Token)
}

func TestTransaction_VerifyResultRoundTrip(t *testing.T) {
	vr := &VerifyResult{
		RefID: "ref-1", CardNumber: "6037****1234", OrderID: "o-1", Amount: 5000,
		RawParams: map[string]string{"RefNum": "rn-1"},
	}

	got := NewTransaction("saman", vr).VerifyResult()

	assert.Equal(t, vr, got)
}

func TestPaymentStore_SetStatusUnknownOrder(t *testing.T) {
	ps, _ := newTestStores(t)

	err := ps.SetStatus(context.Background(), "no-such-order", StatusSucceeded)

	assert.True(t, errorx.IsNotFoundError(err))
}

func TestPaymentStore_SetStatusUnchangedIsNotAnError(t *testing.T) {
	ctx := context.Background()
	ps, _ := newTestStores(t)
	require.NoError(t, ps.SavePayment(ctx, NewStoredPayment("zarinpal", &Payment{
		OrderID: "o-1", Amount: 1000, CallbackURL: "https://x/cb",
	})))

	require.NoError(t, ps.SetStatus(ctx, "o-1", StatusPending))
}

func TestPaymentStore_TransitionStatus(t *testing.T) {
	ctx := context.Background()
	ps, _ := newTestStores(t)
	require.NoError(t, ps.SavePayment(ctx, NewStoredPayment("zarinpal", &Payment{
		OrderID: "o-1", Amount: 1000, CallbackURL: "https://x/cb",
	})))

	ok, err := ps.TransitionStatus(ctx, "o-1", StatusPending, StatusProcessing)
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = ps.TransitionStatus(ctx, "o-1", StatusPending, StatusProcessing)
	require.NoError(t, err)
	assert.False(t, ok, "a payment no longer pending cannot be claimed again")

	ok, err = ps.TransitionStatus(ctx, "missing", StatusPending, StatusProcessing)
	require.NoError(t, err)
	assert.False(t, ok)

	got, err := ps.GetPayment(ctx, "o-1")
	require.NoError(t, err)
	assert.Equal(t, StatusProcessing, got.Status)
}

// Concurrent callbacks for one payment: exactly one may claim it.
func TestPaymentStore_TransitionStatusConcurrent(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	// One connection, so the in-memory database is shared by every goroutine.
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&StoredPayment{}))
	ps := &dbPaymentStore{}
	ps.initDB(db)
	require.NoError(t, ps.SavePayment(ctx, NewStoredPayment("zarinpal", &Payment{
		OrderID: "o-1", Amount: 1000, CallbackURL: "https://x/cb",
	})))

	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := ps.TransitionStatus(ctx, "o-1", StatusPending, StatusProcessing)
			assert.NoError(t, err)
			if ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), wins.Load())
}
