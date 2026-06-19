package payjet

import "github.com/hatami57/microjet/host"

// StoreModule registers payjet's persistence services — a PaymentStore and a
// TransactionStore — with a microjet host app. By default it wires the
// gormx-backed stores, which persist into the host's database (app.DB()), so an
// app that already configured a database gets payment/transaction storage with
// nothing to implement. Substitute your own backend with WithPaymentStore /
// WithTransactionStore.
type StoreModule struct {
	paymentStore     PaymentStore
	transactionStore TransactionStore
}

// ModuleOption customizes a StoreModule.
type ModuleOption func(*StoreModule)

// WithPaymentStore replaces the default gormx PaymentStore with your own.
func WithPaymentStore(s PaymentStore) ModuleOption {
	return func(m *StoreModule) { m.paymentStore = s }
}

// WithTransactionStore replaces the default gormx TransactionStore with your own.
func WithTransactionStore(s TransactionStore) ModuleOption {
	return func(m *StoreModule) { m.transactionStore = s }
}

// Module builds the payjet persistence module. Register it on a host app and
// resolve the stores anywhere from the service container:
//
//	host.MustNew().
//	    WithDatabase(postgres.Driver()).
//	    WithModule(payjet.Module()).
//	    MustRun()
//
//	ps := host.MustResolveService[payjet.PaymentStore](app)
func Module(opts ...ModuleOption) *StoreModule {
	m := &StoreModule{
		paymentStore:     NewDBPaymentStore(),
		transactionStore: NewDBTransactionStore(),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Register implements host.Module. The registered stores run through the normal
// service lifecycle, so the gormx defaults receive the shared *gorm.DB during
// Init and migrate their tables during Setup.
func (m *StoreModule) Register(app *host.App) error {
	app.ProvideService(host.ProvideType(m.paymentStore))
	app.ProvideService(host.ProvideType(m.transactionStore))
	return app.Err()
}
