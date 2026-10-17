package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	koala "github.com/bingyuanng/koala"
	"github.com/bingyuanng/koala/config"
	"github.com/bingyuanng/koala/examples/users/internal/bootstrap"
	"github.com/bingyuanng/koala/examples/users/internal/deps"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := config.Load(config.Options[config.Config]{Defaults: config.DefaultConfig, Validate: bootstrap.ValidateConfig})
	if err != nil {
		log.Fatal(err)
	}
	app, err := koala.New(cfg, koala.Options{OpenAPI: bootstrap.OpenAPI})
	if err != nil {
		log.Fatal(err)
	}
	if err = bootstrap.RegisterAll(app, deps.New()); err != nil {
		log.Fatal(err)
	}
	if err = app.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
