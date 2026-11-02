package fetcher

import "context"

// Backend fetches raw record payloads by ID.
type Backend interface {
	FetchRecord(id string) (string, error)
}

// Service wraps a Backend.
type Service struct {
	backend Backend
}

// NewService creates a new Service.
func NewService(b Backend) *Service {
	return &Service{backend: b}
}

// Get retrieves a record synchronously.
func (s *Service) Get(_ context.Context, id string) (string, error) {
	return s.backend.FetchRecord(id)
}
