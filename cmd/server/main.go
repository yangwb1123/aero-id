package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"

	"github.com/aero/aero-id/internal/app"
	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/pkg/logger"
)

func main() {
	cfgPath := flag.String("config", "configs/config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load config: "+err.Error())
		os.Exit(1)
	}

	log, err := logger.New(cfg.Log.Level, cfg.Log.Encoding)
	if err != nil {
		panic("init logger: " + err.Error())
	}
	defer func() { _ = log.Sync() }()

	// Cancel ctx on SIGINT/SIGTERM so Run shuts down gracefully.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	application, err := app.New(ctx, cfg, log)
	if err != nil {
		log.Fatal("initialize app", zap.Error(err))
	}
	defer application.Close()

	if err := application.Run(ctx); err != nil {
		log.Fatal("run app", zap.Error(err))
	}
	log.Info("server stopped cleanly")
}
