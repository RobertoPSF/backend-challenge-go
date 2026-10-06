package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"go.uber.org/fx"

	"github.com/RobertoPSF/backend-challenge-go/internal/platform/config"
)

var Module = fx.Module("auth", fx.Provide(NewVerifier))

const (
	RoleProvider    = "provider"
	RoleWalletAdmin = "wallet-admin"
)

var ErrInvalidToken = errors.New("invalid token")

type Principal struct {
	Subject    string
	ClientID   string
	ProviderID string
	Roles      []string
}

func (p Principal) HasRole(role string) bool {
	return slices.Contains(p.Roles, role)
}

type Verifier struct {
	verifier *oidc.IDTokenVerifier
}

func NewVerifier(lc fx.Lifecycle, cfg config.Config, log *slog.Logger) *Verifier {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}

	keySet := oidc.NewRemoteKeySet(oidc.ClientContext(context.Background(), client), cfg.OIDC.JWKSURL)
	v := &Verifier{verifier: oidc.NewVerifier(cfg.OIDC.Issuer, keySet, &oidc.Config{
		ClientID:             cfg.OIDC.Audience,
		SupportedSigningAlgs: []string{oidc.RS256},
	})}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := checkJWKS(ctx, client, cfg.OIDC.JWKSURL); err != nil {
				return err
			}
			log.Info("oidc verifier ready", "issuer", cfg.OIDC.Issuer, "audience", cfg.OIDC.Audience)
			return nil
		},
		OnStop: func(context.Context) error {
			transport.CloseIdleConnections()
			return nil
		},
	})
	return v
}

type tokenClaims struct {
	Subject     string `json:"sub"`
	ClientID    string `json:"azp"`
	ProviderID  string `json:"provider_id"`
	RealmAccess struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

func (v *Verifier) Verify(ctx context.Context, rawToken string) (Principal, error) {
	token, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return Principal{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	var claims tokenClaims
	if err := token.Claims(&claims); err != nil {
		return Principal{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	return Principal{
		Subject:    claims.Subject,
		ClientID:   claims.ClientID,
		ProviderID: claims.ProviderID,
		Roles:      claims.RealmAccess.Roles,
	}, nil
}

func checkJWKS(ctx context.Context, client *http.Client, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("jwks request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch jwks %s: %w", url, err)
	}
	defer resp.Body.Close()

	var body struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&body) != nil || len(body.Keys) == 0 {
		return fmt.Errorf("jwks %s: unexpected response (status %d)", url, resp.StatusCode)
	}
	return nil
}

type principalKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}
