package main

import "github.com/aero/aero-id/internal/config"

// verifyConfigChecksum is enabled by the Kubernetes init container through the
// Downward API. Keep the CLI-local name for its tests while sharing the exact
// checksum policy with the server process.
func verifyConfigChecksum(path, expected string) error {
	return config.VerifyFileChecksum(path, expected)
}
