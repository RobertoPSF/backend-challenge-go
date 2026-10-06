package httpapi

import (
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/RobertoPSF/backend-challenge-go/internal/auth"
)

func Authenticate(verifier *auth.Verifier, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
			if !found || !strings.EqualFold(scheme, "Bearer") || token == "" {
				unauthenticated(w, "missing bearer token")
				return
			}

			principal, err := verifier.Verify(r.Context(), token)
			if err != nil {
				log.WarnContext(r.Context(), "authentication failed", "path", r.URL.Path, "reason", err.Error())
				unauthenticated(w, "invalid or expired token")
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
		})
	}
}

func RequireRole(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := auth.PrincipalFrom(r.Context())
			allowed := ok && slices.ContainsFunc(roles, principal.HasRole)
			if !allowed {
				writeError(w, http.StatusForbidden, "FORBIDDEN", "missing required role "+strings.Join(roles, " or "))
				return
			}
			if isProviderOnly(principal) && principal.ProviderID == "" {
				writeError(w, http.StatusForbidden, "FORBIDDEN", "provider identity not bound to this credential")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isProviderOnly(p auth.Principal) bool {
	return p.HasRole(auth.RoleProvider) && !p.HasRole(auth.RoleWalletAdmin)
}

func unauthenticated(w http.ResponseWriter, message string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="wagering", error="invalid_token"`)
	writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", message)
}
