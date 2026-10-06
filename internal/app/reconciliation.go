package app

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/RobertoPSF/backend-challenge-go/internal/domain"
	"github.com/RobertoPSF/backend-challenge-go/internal/store"
)

type Reconciliation struct {
	WalletID          uuid.UUID
	StoredBalance     domain.Money
	CalculatedBalance domain.Money
	Difference        domain.Money
	Consistent        bool
	CheckedEntries    int
}

type Reconciler struct {
	store      *store.Store
	log        *slog.Logger
	mismatches prometheus.Counter
}

func NewReconciler(st *store.Store, reg prometheus.Registerer, log *slog.Logger) *Reconciler {
	mismatches := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "reconciliation_mismatches_total",
		Help: "Reconciliations whose stored balance differs from the balance rebuilt from the ledger.",
	})
	reg.MustRegister(mismatches)
	return &Reconciler{store: st, log: log, mismatches: mismatches}
}

func (s *Reconciler) Reconcile(ctx context.Context, walletID uuid.UUID) (Reconciliation, error) {
	var result Reconciliation
	err := s.store.ReadSnapshot(ctx, func(r *store.Repos) error {
		wallet, err := r.Wallets.Get(ctx, walletID)
		if err != nil {
			return err
		}
		net, entries, err := r.Ledger.Totals(ctx, walletID)
		if err != nil {
			return err
		}
		calculated, err := domain.NewMoney(net, wallet.Currency())
		if err != nil {
			return err
		}
		difference, err := wallet.Balance().Sub(calculated)
		if err != nil {
			return err
		}
		result = Reconciliation{
			WalletID:          walletID,
			StoredBalance:     wallet.Balance(),
			CalculatedBalance: calculated,
			Difference:        difference,
			Consistent:        difference.IsZero(),
			CheckedEntries:    entries,
		}
		return nil
	})
	if err != nil {
		return Reconciliation{}, translate(ctx, err)
	}

	if !result.Consistent {
		s.mismatches.Inc()
		s.log.WarnContext(ctx, "reconciliation mismatch", "walletId", walletID,
			"storedBalance", result.StoredBalance.String(), "calculatedBalance", result.CalculatedBalance.String(),
			"difference", result.Difference.String(), "checkedEntries", result.CheckedEntries)
	}
	return result, nil
}
