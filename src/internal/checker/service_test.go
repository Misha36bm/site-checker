package checker

import (
	"context"
	"errors"
	"testing"

	"github.com/Misha36bm/site-checker/src/internal/database"
	"github.com/Misha36bm/site-checker/src/internal/models"
	"github.com/Misha36bm/site-checker/src/internal/netguard"
)

type fakeStore struct {
	pending  int64
	created  []string
	countErr error
	getErr   error
}

func (f *fakeStore) CreateCheck(_ context.Context, url string) (*models.Check, error) {
	f.created = append(f.created, url)
	return &models.Check{ID: 1, URL: url, Status: models.StatusPending}, nil
}

func (f *fakeStore) GetCheck(_ context.Context, id int64) (*models.Check, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return &models.Check{ID: id}, nil
}

func (f *fakeStore) CountPendingChecks(context.Context) (int64, error) {
	return f.pending, f.countErr
}

var limits = Limits{MaxPendingChecks: 2, MaxURLLength: 2048}

func TestCreateQueuesValidURL(t *testing.T) {
	store := &fakeStore{pending: 1}
	check, err := NewService(store, netguard.NewPolicy(nil), limits).Create(context.Background(), "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	if check.Status != models.StatusPending || len(store.created) != 1 {
		t.Errorf("check = %+v, created = %v", check, store.created)
	}
}

func TestCreateRejectsInvalidURL(t *testing.T) {
	store := &fakeStore{}
	_, err := NewService(store, netguard.NewPolicy(nil), limits).Create(context.Background(), "ftp://example.com")
	if !netguard.IsInvalidURL(err) {
		t.Fatalf("err = %v", err)
	}
	if len(store.created) != 0 {
		t.Error("an invalid URL was stored")
	}
}

func TestCreateRejectsForbiddenAddress(t *testing.T) {
	store := &fakeStore{}
	_, err := NewService(store, netguard.NewPolicy(nil), limits).Create(context.Background(), "http://127.0.0.1/")
	if !netguard.IsBlocked(err) {
		t.Fatalf("err = %v", err)
	}
	if len(store.created) != 0 {
		t.Error("a forbidden URL was stored")
	}
}

func TestCreateRejectsWhenQueueIsFull(t *testing.T) {
	store := &fakeStore{pending: 2}
	_, err := NewService(store, netguard.NewPolicy(nil), limits).Create(context.Background(), "https://example.com")
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("err = %v", err)
	}
	if len(store.created) != 0 {
		t.Error("a check was stored despite a full queue")
	}
}

func TestCreateReportsStoreError(t *testing.T) {
	boom := errors.New("boom")
	_, err := NewService(&fakeStore{countErr: boom}, netguard.NewPolicy(nil), limits).Create(context.Background(), "https://example.com")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestGetTranslatesNotFound(t *testing.T) {
	store := &fakeStore{getErr: database.ErrNotFound}
	if _, err := NewService(store, netguard.NewPolicy(nil), limits).Get(context.Background(), 1); !errors.Is(err, ErrCheckNotFound) {
		t.Fatalf("err = %v", err)
	}
}
