package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/RobertoPSF/backend-challenge-go/internal/app"
	"github.com/RobertoPSF/backend-challenge-go/internal/domain"
)

const (
	defaultLedgerLimit = 50
	maxLedgerLimit     = 200
)

type moneyDTO struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type openWalletRequest struct {
	PlayerID       string    `json:"playerId"`
	InitialBalance *moneyDTO `json:"initialBalance"`
}

type walletResponse struct {
	ID        uuid.UUID    `json:"id"`
	PlayerID  uuid.UUID    `json:"playerId"`
	Balance   domain.Money `json:"balance"`
	Version   int64        `json:"version"`
	CreatedAt time.Time    `json:"createdAt"`
	UpdatedAt time.Time    `json:"updatedAt"`
}

type ledgerEntryResponse struct {
	ID            uuid.UUID        `json:"id"`
	WalletID      uuid.UUID        `json:"walletId"`
	TransactionID uuid.UUID        `json:"transactionId"`
	Direction     domain.Direction `json:"direction"`
	Amount        domain.Money     `json:"amount"`
	BalanceBefore domain.Money     `json:"balanceBefore"`
	BalanceAfter  domain.Money     `json:"balanceAfter"`
	CreatedAt     time.Time        `json:"createdAt"`
}

type ledgerPageResponse struct {
	Items      []ledgerEntryResponse `json:"items"`
	NextCursor *string               `json:"nextCursor"`
}

type WalletHandlers struct {
	wallets *app.Wallets
	log     *slog.Logger
}

func NewWalletHandlers(wallets *app.Wallets, log *slog.Logger) *WalletHandlers {
	return &WalletHandlers{wallets: wallets, log: log}
}

func (h *WalletHandlers) Open(w http.ResponseWriter, r *http.Request) {
	var req openWalletRequest
	if err := decodeJSON(w, r, &req); err != nil {
		badRequest(w, err)
		return
	}
	playerID, err := uuid.Parse(req.PlayerID)
	if err != nil || playerID == uuid.Nil {
		badRequest(w, fmt.Errorf("%w: playerId must be a non-nil UUID", errInvalidRequest))
		return
	}
	if req.InitialBalance == nil {
		badRequest(w, fmt.Errorf("%w: initialBalance is required", errInvalidRequest))
		return
	}
	initial, err := domain.ParseMoney(req.InitialBalance.Amount, req.InitialBalance.Currency)
	if err != nil {
		writeAppError(w, r, h.log, err)
		return
	}

	wallet, err := h.wallets.Open(r.Context(), playerID, initial, correlationFrom(r.Context()))
	if err != nil {
		writeAppError(w, r, h.log, err)
		return
	}
	h.log.InfoContext(r.Context(), "wallet opened", "walletId", wallet.ID(), "correlationId", correlationFrom(r.Context()))
	w.Header().Set("Location", "/wallets/"+wallet.ID().String())
	writeJSON(w, http.StatusCreated, toWalletResponse(wallet))
}

func (h *WalletHandlers) Get(w http.ResponseWriter, r *http.Request) {
	id, err := uuidParam(r, "walletId")
	if err != nil {
		badRequest(w, err)
		return
	}
	wallet, err := h.wallets.Get(r.Context(), id)
	if err != nil {
		writeAppError(w, r, h.log, err)
		return
	}
	writeJSON(w, http.StatusOK, toWalletResponse(wallet))
}

func (h *WalletHandlers) Ledger(w http.ResponseWriter, r *http.Request) {
	id, err := uuidParam(r, "walletId")
	if err != nil {
		badRequest(w, err)
		return
	}
	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		badRequest(w, err)
		return
	}
	cursor, err := decodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		badRequest(w, err)
		return
	}

	page, err := h.wallets.Ledger(r.Context(), id, cursor, limit)
	if err != nil {
		writeAppError(w, r, h.log, err)
		return
	}

	resp := ledgerPageResponse{Items: make([]ledgerEntryResponse, 0, len(page.Entries))}
	for _, e := range page.Entries {
		resp.Items = append(resp.Items, ledgerEntryResponse{
			ID: e.ID(), WalletID: e.WalletID(), TransactionID: e.TransactionID(), Direction: e.Direction(),
			Amount: e.Amount(), BalanceBefore: e.BalanceBefore(), BalanceAfter: e.BalanceAfter(), CreatedAt: e.CreatedAt(),
		})
	}
	if page.Next != nil {
		next := encodeCursor(*page.Next)
		resp.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, resp)
}

func toWalletResponse(w *domain.Wallet) walletResponse {
	return walletResponse{
		ID: w.ID(), PlayerID: w.PlayerID(), Balance: w.Balance(), Version: w.Version(),
		CreatedAt: w.CreatedAt(), UpdatedAt: w.UpdatedAt(),
	}
}

func parseLimit(raw string) (int, error) {
	if raw == "" {
		return defaultLedgerLimit, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > maxLedgerLimit {
		return 0, fmt.Errorf("%w: limit must be an integer between 1 and %d", errInvalidRequest, maxLedgerLimit)
	}
	return limit, nil
}

type cursorPayload struct {
	CreatedAt time.Time `json:"t"`
	ID        uuid.UUID `json:"id"`
}

func encodeCursor(c app.LedgerCursor) string {
	raw, _ := json.Marshal(cursorPayload{CreatedAt: c.CreatedAt, ID: c.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(raw string) (*app.LedgerCursor, error) {
	if raw == "" {
		return nil, nil
	}
	invalid := fmt.Errorf("%w: invalid cursor", errInvalidRequest)
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, invalid
	}
	var p cursorPayload
	if err := json.Unmarshal(data, &p); err != nil || p.ID == uuid.Nil || p.CreatedAt.IsZero() {
		return nil, invalid
	}
	return &app.LedgerCursor{CreatedAt: p.CreatedAt, ID: p.ID}, nil
}
