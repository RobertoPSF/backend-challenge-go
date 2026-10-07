package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/RobertoPSF/backend-challenge-go/internal/app"
	"github.com/RobertoPSF/backend-challenge-go/internal/auth"
	"github.com/RobertoPSF/backend-challenge-go/internal/domain"
)

const idempotencyHeader = "Idempotency-Key"

type wagerResponse struct {
	TransactionID    uuid.UUID          `json:"transactionId"`
	Status           domain.Status      `json:"status"`
	Balance          *domain.Money      `json:"balance,omitempty"`
	FailureCode      domain.FailureCode `json:"failureCode,omitempty"`
	IdempotentReplay bool               `json:"idempotentReplay"`
}

type transactionResponse struct {
	TransactionID                  uuid.UUID          `json:"transactionId"`
	ProviderID                     string             `json:"providerId,omitempty"`
	ExternalTransactionID          string             `json:"externalTransactionId,omitempty"`
	IdempotencyKey                 string             `json:"idempotencyKey,omitempty"`
	WalletID                       uuid.UUID          `json:"walletId"`
	PlayerID                       uuid.UUID          `json:"playerId"`
	RoundID                        string             `json:"roundId,omitempty"`
	GameID                         string             `json:"gameId,omitempty"`
	Kind                           domain.Kind        `json:"kind"`
	Money                          domain.Money       `json:"money"`
	Status                         domain.Status      `json:"status"`
	FailureCode                    domain.FailureCode `json:"failureCode,omitempty"`
	Balance                        *domain.Money      `json:"balance,omitempty"`
	ReferenceExternalTransactionID string             `json:"referenceExternalTransactionId,omitempty"`
	ReferenceTransactionID         *uuid.UUID         `json:"referenceTransactionId,omitempty"`
	Attempts                       int                `json:"attempts"`
	NextAttemptAt                  *time.Time         `json:"nextAttemptAt,omitempty"`
	ExpiresAt                      *time.Time         `json:"expiresAt,omitempty"`
	CreatedAt                      time.Time          `json:"createdAt"`
	UpdatedAt                      time.Time          `json:"updatedAt"`
	ProcessedAt                    *time.Time         `json:"processedAt,omitempty"`
}

type WagerHandlers struct {
	wagers *app.Wagers
	log    *slog.Logger
}

func NewWagerHandlers(wagers *app.Wagers, log *slog.Logger) *WagerHandlers {
	return &WagerHandlers{wagers: wagers, log: log}
}

func (h *WagerHandlers) Submit(w http.ResponseWriter, r *http.Request) {
	principal, _ := auth.PrincipalFrom(r.Context())

	key := r.Header.Get(idempotencyHeader)
	if key == "" {
		writeError(w, http.StatusBadRequest, "MISSING_IDEMPOTENCY_KEY", "the Idempotency-Key header is required")
		return
	}
	var input domain.WagerRequestInput
	if err := decodeJSON(w, r, &input); err != nil {
		badRequest(w, err)
		return
	}
	if input.ProviderID != principal.ProviderID {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "providerId does not match the authenticated provider")
		return
	}
	req, err := domain.ParseWagerRequest(input, key)
	if err != nil {
		writeAppError(w, r, h.log, err)
		return
	}

	result, err := h.wagers.Process(r.Context(), app.WagerCommand{Request: req, CorrelationID: correlationFrom(r.Context()), Channel: app.ChannelHTTP})
	if err != nil {
		if errors.Is(err, domain.ErrWalletNotFound) {
			h.log.WarnContext(r.Context(), "wager refused: wallet not found", "walletId", input.WalletID,
				"providerId", req.ProviderID, "correlationId", correlationFrom(r.Context()))
		}
		writeAppError(w, r, h.log, err)
		return
	}

	s := result.Transaction.Snapshot()
	h.log.InfoContext(r.Context(), "wager transaction handled",
		"transactionId", s.ID, "walletId", s.WalletID, "providerId", req.ProviderID, "kind", s.Kind,
		"status", s.Status, "failureCode", s.FailureCode, "idempotentReplay", result.Replay,
		"correlationId", correlationFrom(r.Context()))

	if s.Status == domain.StatusPendingReference || s.Status == domain.StatusPending {
		w.Header().Set("Location", "/wagering/transactions/"+s.ID.String())
	}
	writeJSON(w, submitStatus(s.Status, result.Replay), wagerResponse{
		TransactionID:    s.ID,
		Status:           s.Status,
		Balance:          s.BalanceAfter,
		FailureCode:      s.FailureCode,
		IdempotentReplay: result.Replay,
	})
}

func submitStatus(status domain.Status, replay bool) int {
	switch status {
	case domain.StatusProcessed:
		if replay {
			return http.StatusOK
		}
		return http.StatusCreated
	case domain.StatusRejected:
		return http.StatusUnprocessableEntity
	case domain.StatusPending, domain.StatusPendingReference:
		return http.StatusAccepted
	default:
		return http.StatusInternalServerError
	}
}

func (h *WagerHandlers) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuidParam(r, "transactionId")
	if err != nil {
		badRequest(w, err)
		return
	}
	view, err := h.wagers.Get(r.Context(), id)
	if err != nil {
		writeAppError(w, r, h.log, err)
		return
	}
	principal, _ := auth.PrincipalFrom(r.Context())
	if isProviderOnly(principal) && providerOf(view) != principal.ProviderID {
		writeAppError(w, r, h.log, app.ErrNotFound)
		return
	}
	writeJSON(w, http.StatusOK, toTransactionResponse(view))
}

func (h *WagerHandlers) GetByExternalID(w http.ResponseWriter, r *http.Request) {
	providerID := chi.URLParam(r, "providerId")
	principal, _ := auth.PrincipalFrom(r.Context())
	if isProviderOnly(principal) && providerID != principal.ProviderID {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "providers can only read their own transactions")
		return
	}
	view, err := h.wagers.GetByExternalID(r.Context(), providerID, chi.URLParam(r, "externalTransactionId"))
	if err != nil {
		writeAppError(w, r, h.log, err)
		return
	}
	writeJSON(w, http.StatusOK, toTransactionResponse(view))
}

func providerOf(v app.TransactionView) string {
	if ext := v.Transaction.Snapshot().External; ext != nil {
		return ext.ProviderID
	}
	return ""
}

func toTransactionResponse(v app.TransactionView) transactionResponse {
	s := v.Transaction.Snapshot()
	resp := transactionResponse{
		TransactionID: s.ID, WalletID: s.WalletID, PlayerID: s.PlayerID, Kind: s.Kind, Money: s.Money,
		Status: s.Status, FailureCode: s.FailureCode, Balance: s.BalanceAfter,
		ReferenceTransactionID: s.ReferenceTransactionID, Attempts: v.Attempts,
		CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, ProcessedAt: s.ProcessedAt,
	}
	if !s.Status.IsTerminal() {
		resp.NextAttemptAt, resp.ExpiresAt = v.NextAttemptAt, v.ExpiresAt
	}
	if ext := s.External; ext != nil {
		resp.ProviderID, resp.ExternalTransactionID, resp.IdempotencyKey = ext.ProviderID, ext.ExternalTransactionID, ext.IdempotencyKey
		resp.RoundID, resp.GameID, resp.ReferenceExternalTransactionID = ext.RoundID, ext.GameID, ext.ReferenceExternalTransactionID
	}
	return resp
}
