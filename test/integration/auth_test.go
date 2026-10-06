//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/fx/fxtest"

	"github.com/RobertoPSF/backend-challenge-go/internal/auth"
	"github.com/RobertoPSF/backend-challenge-go/internal/httpapi"
	"github.com/RobertoPSF/backend-challenge-go/internal/platform/config"
	"github.com/RobertoPSF/backend-challenge-go/test/testinfra"
)

func newVerifier(t *testing.T, kc testinfra.Keycloak, issuer string) *auth.Verifier {
	t.Helper()
	lc := fxtest.NewLifecycle(t)
	v := auth.NewVerifier(lc, config.Config{OIDC: config.OIDC{Issuer: issuer, JWKSURL: kc.JWKSURL, Audience: "wagering-api"}}, silentLog)
	lc.RequireStart()
	t.Cleanup(lc.RequireStop)
	return v
}

func protectedServer(t *testing.T, v *auth.Verifier) *httptest.Server {
	t.Helper()
	whoAmI := func(w http.ResponseWriter, r *http.Request) {
		p, _ := auth.PrincipalFrom(r.Context())
		_ = json.NewEncoder(w).Encode(map[string]string{"providerId": p.ProviderID, "clientId": p.ClientID})
	}
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(httpapi.Authenticate(v, silentLog))
		r.With(httpapi.RequireRole(auth.RoleProvider)).Get("/provider", whoAmI)
		r.With(httpapi.RequireRole(auth.RoleWalletAdmin)).Get("/admin", whoAmI)
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

type authResult struct {
	status int
	body   map[string]any
	header http.Header
}

func call(t *testing.T, url, authorization string) authResult {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return authResult{status: resp.StatusCode, body: body, header: resp.Header}
}

func TestAuth_RealKeycloak(t *testing.T) {
	kc := testinfra.StartKeycloak(t)
	srv := protectedServer(t, newVerifier(t, kc, kc.Issuer))

	providerA := kc.Token(t, "provider-a")
	walletService := kc.Token(t, "wallet-service")

	t.Run("provider is authenticated and bound to its providerId", func(t *testing.T) {
		res := call(t, srv.URL+"/provider", "Bearer "+providerA)
		if res.status != http.StatusOK || res.body["providerId"] != "provider-a" || res.body["clientId"] != "provider-a" {
			t.Fatalf("got %d %v", res.status, res.body)
		}
		res = call(t, srv.URL+"/provider", "Bearer "+kc.Token(t, "provider-b"))
		if res.status != http.StatusOK || res.body["providerId"] != "provider-b" {
			t.Fatalf("provider-b got %d %v", res.status, res.body)
		}
	})

	t.Run("internal service reaches internal routes", func(t *testing.T) {
		if res := call(t, srv.URL+"/admin", "Bearer "+walletService); res.status != http.StatusOK {
			t.Fatalf("got %d %v", res.status, res.body)
		}
	})

	unauthenticated := map[string]string{
		"missing header":     "",
		"not a bearer token": "Basic cHJvdmlkZXItYTpzZWNyZXQ=",
		"empty bearer":       "Bearer ",
		"malformed token":    "Bearer not-a-jwt",
		"tampered signature": "Bearer " + providerA[:len(providerA)-4] + "AAAA",
		"tampered payload":   "Bearer " + tamperPayload(providerA),
		"wrong audience":     "Bearer " + kc.Token(t, "other-api-client"),
	}
	for name, header := range unauthenticated {
		t.Run("401 "+name, func(t *testing.T) {
			res := call(t, srv.URL+"/provider", header)
			if res.status != http.StatusUnauthorized || res.body["error"].(map[string]any)["code"] != "UNAUTHENTICATED" {
				t.Fatalf("got %d %v", res.status, res.body)
			}
			if res.header.Get("WWW-Authenticate") == "" {
				t.Error("missing WWW-Authenticate header")
			}
		})
	}

	t.Run("401 token from another issuer", func(t *testing.T) {
		other := protectedServer(t, newVerifier(t, kc, "http://evil.example/realms/wagering"))
		if res := call(t, other.URL+"/provider", "Bearer "+providerA); res.status != http.StatusUnauthorized {
			t.Fatalf("got %d", res.status)
		}
	})

	t.Run("401 expired token", func(t *testing.T) {
		short := kc.Token(t, "provider-a-short-lived")
		if res := call(t, srv.URL+"/provider", "Bearer "+short); res.status != http.StatusOK {
			t.Fatalf("fresh short-lived token got %d", res.status)
		}
		time.Sleep(3 * time.Second)
		if res := call(t, srv.URL+"/provider", "Bearer "+short); res.status != http.StatusUnauthorized {
			t.Fatalf("expired token got %d", res.status)
		}
	})

	forbidden := map[string]struct{ path, token string }{
		"provider on internal route":         {"/admin", providerA},
		"internal service on provider route": {"/provider", walletService},
		"no roles on provider route":         {"/provider", kc.Token(t, "no-role-client")},
		"no roles on internal route":         {"/admin", kc.Token(t, "no-role-client")},
	}
	for name, tc := range forbidden {
		t.Run("403 "+name, func(t *testing.T) {
			res := call(t, srv.URL+tc.path, "Bearer "+tc.token)
			if res.status != http.StatusForbidden || res.body["error"].(map[string]any)["code"] != "FORBIDDEN" {
				t.Fatalf("got %d %v", res.status, res.body)
			}
		})
	}
}

func TestAuth_VerifierFailsToStartWithoutJWKS(t *testing.T) {
	lc := fxtest.NewLifecycle(t)
	auth.NewVerifier(lc, config.Config{OIDC: config.OIDC{
		Issuer: testinfra.KeycloakIssuer, JWKSURL: "http://127.0.0.1:1/certs", Audience: "wagering-api",
	}}, silentLog)
	if err := lc.Start(t.Context()); err == nil {
		t.Fatal("start must fail when the JWKS endpoint is unreachable")
	}
}

func tamperPayload(token string) string {
	header, rest, _ := strings.Cut(token, ".")
	payload, signature, _ := strings.Cut(rest, ".")
	b := []byte(payload)
	if b[10] == 'A' {
		b[10] = 'B'
	} else {
		b[10] = 'A'
	}
	return header + "." + string(b) + "." + signature
}
