package service

import "github.com/aero/aero-id/internal/repository"

// Rule (c) negative fixture: service -> repository is the legal downward
// dependency (handler -> service -> repository). repository must not import
// service, but service may import repository.
type Service struct {
	repo *repository.Repository
}
