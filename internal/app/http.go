package app

import (
	"fmt"
	"net/http"

	"github.com/aero/aero-id/internal/config"
)

func buildHTTPServer(cfg *config.Config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              fmt.Sprintf("%s:%d", cfg.Server.HTTP.Host, cfg.Server.HTTP.Port),
		Handler:           handler,
		ReadTimeout:       cfg.Server.HTTP.ReadTimeout,
		ReadHeaderTimeout: cfg.Server.HTTP.ReadHeaderTimeout,
		WriteTimeout:      cfg.Server.HTTP.WriteTimeout,
	}
}
