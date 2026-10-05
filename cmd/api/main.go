package main

import (
	"go.uber.org/fx"

	"github.com/RobertoPSF/backend-challenge-go/internal/bootstrap"
)

func main() {
	fx.New(bootstrap.Options()).Run()
}
