package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/RobertoPSF/backend-challenge-go/internal/app"
	"github.com/RobertoPSF/backend-challenge-go/internal/domain"
)

type errorDetail struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	CorrelationID string `json:"correlationId,omitempty"`
}

type errorBody struct {
	Error errorDetail `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: errorDetail{
		Code:          code,
		Message:       message,
		CorrelationID: w.Header().Get(correlationHeader),
	}})
}

func writeAppError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	var domainErr *domain.DomainError
	switch {
	case errors.As(err, &domainErr) && domainErr.Kind == domain.KindValidation:
		writeError(w, http.StatusBadRequest, string(domainErr.Code), err.Error())
	case errors.As(err, &domainErr) && domainErr.Kind == domain.KindConflict:
		writeError(w, http.StatusConflict, string(domainErr.Code), domainErr.Message)
	case errors.As(err, &domainErr) && domainErr.Kind == domain.KindBusiness:
		writeError(w, http.StatusUnprocessableEntity, string(domainErr.Code), err.Error())
	case errors.Is(err, app.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "resource not found")
	case errors.Is(err, app.ErrUnavailable):
		log.WarnContext(r.Context(), "dependency unavailable", "error", err)
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "TEMPORARILY_UNAVAILABLE", "temporarily unavailable, retry later")
	case errors.Is(err, context.Canceled):
		log.InfoContext(r.Context(), "request cancelled by the client")
	default:
		log.ErrorContext(r.Context(), "unexpected error", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "unexpected error")
	}
}
