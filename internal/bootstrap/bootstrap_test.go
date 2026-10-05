package bootstrap

import (
	"testing"

	"go.uber.org/fx"
)

func TestOptions_DependencyGraphIsComplete(t *testing.T) {
	if err := fx.ValidateApp(Options()); err != nil {
		t.Fatalf("invalid fx graph: %v", err)
	}
}
