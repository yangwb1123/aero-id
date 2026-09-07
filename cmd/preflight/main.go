// Command preflight verifies release-time source trust before API or worker
// pods are started. It uses the same config loader and connector code as the
// server, but performs no database or application wiring.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/aero/aero-id/internal/app"
	"github.com/aero/aero-id/internal/config"
)

func main() {
	configPath := flag.String("config", "configs/config.yaml", "path to config file")
	flag.Parse()
	if err := run(*configPath); err != nil {
		fmt.Fprintln(os.Stderr, "preflight: "+err.Error())
		os.Exit(1)
	}
}

func run(configPath string) error {
	// Kubernetes supplies the pod-template checksum through the Downward API;
	// checking it before Load prevents a mutable ConfigMap from bypassing the
	// rollout contract. Standalone invocations may omit the variable.
	if err := verifyConfigChecksum(configPath, os.Getenv("AERO_ID_CONFIG_SHA256")); err != nil {
		return err
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	return app.PreflightAeroIMTargetAssertion(cfg)
}
