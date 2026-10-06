//go:build integration

package testinfra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/localstack"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	PostgresImage   = "postgres:16.15-alpine"
	LocalStackImage = "localstack/localstack:4.14.0"
	KeycloakImage   = "quay.io/keycloak/keycloak:26.8.0"
	KeycloakIssuer  = "http://localhost:8081/realms/wagering"

	database      = "wallet"
	ownerPassword = "wallet_owner_test"
	appPassword   = "wallet_app_test"
)

type Postgres struct {
	AppURL   string
	OwnerURL string
}

type LocalStack struct {
	Endpoint string
}

func RepoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func StartPostgres(t testing.TB) Postgres {
	t.Helper()
	ctx := context.Background()

	container, err := postgres.Run(ctx, PostgresImage,
		postgres.WithDatabase(database),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
		postgres.WithInitScripts(filepath.Join(RepoRoot(), "deploy", "postgres", "init-roles.sh")),
		testcontainers.WithEnv(map[string]string{
			"WALLET_OWNER_PASSWORD": ownerPassword,
			"WALLET_APP_PASSWORD":   appPassword,
		}),
		postgres.BasicWaitStrategies(),
	)
	testcontainers.CleanupContainer(t, container)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}

	superURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres connection string: %v", err)
	}
	pg := Postgres{
		OwnerURL: strings.Replace(superURL, "postgres:postgres@", "wallet_owner:"+ownerPassword+"@", 1),
		AppURL:   strings.Replace(superURL, "postgres:postgres@", "wallet_app:"+appPassword+"@", 1),
	}

	if err := MigrateUp(pg.OwnerURL); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	return pg
}

func MigrateUp(ownerURL string) error {
	return runMigrations(ownerURL, (*migrate.Migrate).Up)
}

func MigrateDownAll(ownerURL string) error {
	return runMigrations(ownerURL, (*migrate.Migrate).Down)
}

func runMigrations(ownerURL string, run func(*migrate.Migrate) error) error {
	m, err := migrate.New("file://"+filepath.Join(RepoRoot(), "migrations"), strings.Replace(ownerURL, "postgres://", "pgx5://", 1))
	if err != nil {
		return err
	}
	defer m.Close()
	if err := run(m); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

func StartLocalStack(t testing.TB) LocalStack {
	t.Helper()
	ctx := context.Background()

	container, err := localstack.Run(ctx, LocalStackImage,
		testcontainers.WithEnv(map[string]string{"SERVICES": "sqs"}),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			HostFilePath:      filepath.Join(RepoRoot(), "deploy", "aws", "init-queues.sh"),
			ContainerFilePath: "/etc/localstack/init/ready.d/init-queues.sh",
			FileMode:          0o755,
		}),
		testcontainers.WithAdditionalWaitStrategy(wait.ForExec([]string{"test", "-f", "/tmp/queues-ready"})),
	)
	testcontainers.CleanupContainer(t, container)
	if err != nil {
		t.Fatalf("start localstack: %v", err)
	}

	endpoint, err := container.PortEndpoint(ctx, "4566/tcp", "http")
	if err != nil {
		t.Fatalf("localstack endpoint: %v", err)
	}
	return LocalStack{Endpoint: endpoint}
}

func FreeAddr(t testing.TB) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer ln.Close()
	return fmt.Sprintf("127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port)
}

type Keycloak struct {
	BaseURL string
	Issuer  string
	JWKSURL string
}

func StartKeycloak(t testing.TB) Keycloak {
	t.Helper()
	ctx := context.Background()

	container, err := testcontainers.Run(ctx, KeycloakImage,
		testcontainers.WithExposedPorts("8080/tcp", "9000/tcp"),
		testcontainers.WithCmd("start-dev", "--import-realm"),
		testcontainers.WithEnv(map[string]string{
			"KC_HEALTH_ENABLED":               "true",
			"KC_HOSTNAME":                     "http://localhost:8081",
			"KC_HOSTNAME_BACKCHANNEL_DYNAMIC": "true",
		}),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			HostFilePath:      filepath.Join(RepoRoot(), "deploy", "keycloak", "realm-wagering.json"),
			ContainerFilePath: "/opt/keycloak/data/import/realm-wagering.json",
			FileMode:          0o644,
		}),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/health/ready").WithPort("9000/tcp").WithStartupTimeout(3*time.Minute)),
	)
	testcontainers.CleanupContainer(t, container)
	if err != nil {
		t.Fatalf("start keycloak: %v", err)
	}

	endpoint, err := container.PortEndpoint(ctx, "8080/tcp", "http")
	if err != nil {
		t.Fatalf("keycloak endpoint: %v", err)
	}
	return Keycloak{
		BaseURL: endpoint,
		Issuer:  KeycloakIssuer,
		JWKSURL: endpoint + "/realms/wagering/protocol/openid-connect/certs",
	}
}

func (k Keycloak) Token(t testing.TB, clientID string) string {
	t.Helper()
	form := url.Values{"grant_type": {"client_credentials"}}
	req, _ := http.NewRequest(http.MethodPost, k.BaseURL+"/realms/wagering/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(clientID, clientID+"-local-secret")
	req.Close = true

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("token for %s: %v", clientID, err)
	}
	defer resp.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.AccessToken == "" {
		t.Fatalf("token for %s: status %d", clientID, resp.StatusCode)
	}
	return body.AccessToken
}
