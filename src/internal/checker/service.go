// Package checker holds the business logic of website checks.
package checker

import (
	"context"
	"errors"
	"fmt"

	"github.com/Misha36bm/site-checker/src/internal/database"
	"github.com/Misha36bm/site-checker/src/internal/models"
	"github.com/Misha36bm/site-checker/src/internal/netguard"
)

var (
	// ErrQueueFull means too many checks are already waiting.
	ErrQueueFull = errors.New("too many pending checks")
	// ErrCheckNotFound means the check does not exist.
	ErrCheckNotFound = errors.New("check not found")
)

// Store is the part of the repository the service needs.
type Store interface {
	CreateCheck(ctx context.Context, url string) (*models.Check, error)
	GetCheck(ctx context.Context, id int64) (*models.Check, error)
	CountPendingChecks(ctx context.Context) (int64, error)
}

// Limits restricts what the service accepts.
type Limits struct {
	MaxPendingChecks int
	MaxURLLength     int
}

// Service creates and retrieves checks.
type Service struct {
	store  Store
	policy *netguard.Policy
	limits Limits
}

// NewService returns a Service.
func NewService(store Store, policy *netguard.Policy, limits Limits) *Service {
	return &Service{store: store, policy: policy, limits: limits}
}

// Create validates url and queues a check. It returns a netguard.InvalidURLError
// for a rejected URL and ErrQueueFull if the queue is at its limit. The limit is
// approximate: concurrent requests can pass it slightly, which is harmless.
func (s *Service) Create(ctx context.Context, url string) (*models.Check, error) {
	if err := s.policy.ValidateURL(url, s.limits.MaxURLLength); err != nil {
		return nil, err
	}
	pending, err := s.store.CountPendingChecks(ctx)
	if err != nil {
		return nil, fmt.Errorf("count pending checks: %w", err)
	}
	if pending >= int64(s.limits.MaxPendingChecks) {
		return nil, ErrQueueFull
	}
	check, err := s.store.CreateCheck(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("create check: %w", err)
	}
	return check, nil
}

// Get returns a check, or ErrCheckNotFound.
func (s *Service) Get(ctx context.Context, id int64) (*models.Check, error) {
	check, err := s.store.GetCheck(ctx, id)
	if errors.Is(err, database.ErrNotFound) {
		return nil, ErrCheckNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get check: %w", err)
	}
	return check, nil
}
