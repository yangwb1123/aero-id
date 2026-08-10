package repository

import "github.com/aero/aero-id/internal/service"

// Rule (c) positive fixture: internal/repository/ must not import
// internal/service (dependency flow is handler -> service -> repository).
