//go:build integration

package testinfra

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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
	m, err := migrate.New("file://"+filepath.Join(RepoRoot(), "migrations"), strings.Replace(ownerURL, "postgres://", "pgx5://", 1))
	if err != nil {
		return err
	}
	defer m.Close()
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
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
