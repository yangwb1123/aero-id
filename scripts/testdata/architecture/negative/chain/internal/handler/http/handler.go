package http

import "github.com/aero/aero-id/internal/service"

// Rule (e) negative fixture: the legal handler -> service -> repository
// chain. A handler importing internal/service (not repository) is fine.
func handler(s *service.Service) {}
