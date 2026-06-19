// Command storage demonstrates payjet's persistence interfaces by implementing
// them over a plain in-memory map — the "bring your own storage" path. It needs
// no database and exits when done.
//
// When you run on microjet's database stack you don't write any of this:
// payjet.Module() registers gormx-backed PaymentStore and TransactionStore that
// persist into the host's database. See examples/webshop for that wiring; this
// example shows the interfaces you'd implement to use a different backend.
//
//	go run .
package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/majid/payjet"
)

// memStore implements both payjet.PaymentStore and payjet.TransactionStore over
// in-memory maps. Any backend (Redis, Mongo, a SQL table of your own) works the
// same way — just satisfy the interfaces.
type memStore struct {
	mu      sync.Mutex
	byOrder map[string]*payjet.StoredPayment
	byToken map[string]*payjet.StoredPayment
	txs     map[string][]*payjet.Transaction
}

func newMemStore() *memStore {
	return &memStore{
		byOrder: map[string]*payjet.StoredPayment{},
		byToken: map[string]*payjet.StoredPayment{},
		txs:     map[string][]*payjet.Transaction{},
	}
}

func (s *memStore) SavePayment(_ context.Context, p *payjet.StoredPayment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *p
	s.byOrder[cp.OrderID] = &cp
	if cp.Token != "" {
		s.byToken[cp.Token] = &cp
	}
	return nil
}

func (s *memStore) GetPayment(_ context.Context, orderID string) (*payjet.StoredPayment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byOrder[orderID], nil // nil when absent, as the contract requires
}

func (s *memStore) GetPaymentByToken(_ context.Context, token string) (*payjet.StoredPayment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byToken[token], nil
}

func (s *memStore) SetStatus(_ context.Context, orderID string, status payjet.PaymentStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.byOrder[orderID]; p != nil {
		p.Status = status
	}
	return nil
}

func (s *memStore) SaveTransaction(_ context.Context, t *payjet.Transaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *t
	s.txs[cp.OrderID] = append(s.txs[cp.OrderID], &cp)
	return nil
}

func (s *memStore) GetTransaction(_ context.Context, orderID string) (*payjet.Transaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.txs[orderID]
	if len(list) == 0 {
		return nil, nil
	}
	return list[len(list)-1], nil // newest
}

func (s *memStore) ListTransactions(_ context.Context, orderID string) ([]*payjet.Transaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.txs[orderID], nil
}

// Compile-time proof the backend satisfies both store interfaces.
var (
	_ payjet.PaymentStore     = (*memStore)(nil)
	_ payjet.TransactionStore = (*memStore)(nil)
)

func main() {
	ctx := context.Background()
	store := newMemStore()

	// 1) Persist a pending payment built from a Payment with the helper.
	p := &payjet.Payment{OrderID: "order-3003", Amount: 500_000, CallbackURL: "https://shop.example/cb"}
	sp := payjet.NewStoredPayment("zarinpal", p)
	sp.Token = "A0000000000000000000000000000098765" // from RequestResult.Token
	if err := store.SavePayment(ctx, sp); err != nil {
		panic(err)
	}

	// 2) A token-only callback (Zarinpal) is resolved back to the order.
	found, _ := store.GetPaymentByToken(ctx, sp.Token)
	fmt.Printf("looked up %s via token, status=%s\n", found.OrderID, found.Status)

	// 3) After Verify: mark the order succeeded and record the transaction.
	vr := &payjet.VerifyResult{
		OrderID:   p.OrderID,
		RefID:     "REF-7788",
		Amount:    p.Amount,
		RawParams: map[string]string{"Status": "OK", "Authority": sp.Token},
	}
	_ = store.SetStatus(ctx, p.OrderID, payjet.StatusSucceeded)
	_ = store.SaveTransaction(ctx, payjet.NewTransaction("zarinpal", vr))

	// 4) Read the results back.
	final, _ := store.GetPayment(ctx, p.OrderID)
	tx, _ := store.GetTransaction(ctx, p.OrderID)
	fmt.Printf("final status=%s\n", final.Status)
	fmt.Printf("transaction: gateway=%s refID=%s amount=%d\n", tx.Gateway, tx.RefID, tx.Amount)
}
