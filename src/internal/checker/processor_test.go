package checker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/Misha36bm/site-checker/src/internal/models"
)

type fakeResults struct {
	http        *models.HTTPResult
	metadata    *models.MetadataResult
	httpErr     error
	metadataErr error
}

func (f *fakeResults) SaveHTTPResult(_ context.Context, _ models.Lease, r models.HTTPResult) error {
	f.http = &r
	return f.httpErr
}

func (f *fakeResults) SaveMetadataResult(_ context.Context, _ models.Lease, r models.MetadataResult) error {
	f.metadata = &r
	return f.metadataErr
}

func process(store ResultStore, url string) models.CheckOutcome {
	return NewProcessor(store, newChecker(10)).Process(context.Background(), &models.Check{ID: 1, URL: url, Attempts: 1})
}

func TestProcessHTMLPage(t *testing.T) {
	s := newSite(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<title>Hello</title><meta name="description" content="World">`)
	})
	store := &fakeResults{}
	outcome := process(store, s.at("a.test", "/"))

	if outcome.Status != models.StatusCompleted || outcome.MetadataStatus != models.StageSuccess || outcome.ErrorCode != "" {
		t.Errorf("outcome = %+v", outcome)
	}
	if store.http == nil || !store.http.IsAvailable {
		t.Errorf("HTTP result = %+v", store.http)
	}
	if store.metadata == nil || store.metadata.Title != "Hello" || store.metadata.Description != "World" {
		t.Errorf("metadata = %+v", store.metadata)
	}
}

func TestProcessErrorPageStillCompletes(t *testing.T) {
	s := newSite(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `<title>Not found</title>`)
	})
	store := &fakeResults{}
	outcome := process(store, s.at("a.test", "/"))

	if outcome.Status != models.StatusCompleted || outcome.MetadataStatus != models.StageSuccess {
		t.Errorf("outcome = %+v", outcome)
	}
	if store.http.IsAvailable || *store.http.StatusCode != http.StatusNotFound {
		t.Errorf("HTTP result = %+v", store.http)
	}
	if store.metadata.Title != "Not found" {
		t.Errorf("metadata = %+v", store.metadata)
	}
}

func TestProcessNonHTMLSkipsMetadata(t *testing.T) {
	s := newSite(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
	})
	store := &fakeResults{}
	outcome := process(store, s.at("a.test", "/"))

	if outcome.Status != models.StatusCompleted || outcome.MetadataStatus != models.StageSkipped {
		t.Errorf("outcome = %+v", outcome)
	}
	if store.metadata != nil {
		t.Errorf("metadata = %+v", store.metadata)
	}
}

func TestProcessFailedRequestFailsCheckButKeepsPartialResult(t *testing.T) {
	s := newSite(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/", http.StatusFound)
	})
	store := &fakeResults{}
	outcome := process(store, s.at("a.test", "/"))

	if outcome.Status != models.StatusFailed || outcome.ErrorCode != models.ErrorRedirectLoop || outcome.ErrorMessage == "" {
		t.Errorf("outcome = %+v", outcome)
	}
	if outcome.MetadataStatus != models.StageSkipped || outcome.ScreenshotStatus != models.StageSkipped {
		t.Errorf("stage statuses = %+v", outcome)
	}
	if store.http == nil || len(store.http.RedirectChain) != 1 {
		t.Errorf("the partial chain was not saved: %+v", store.http)
	}
}

func TestProcessReportsFailureToSaveHTTPResult(t *testing.T) {
	s := newSite(t, func(http.ResponseWriter, *http.Request) {})
	outcome := process(&fakeResults{httpErr: errors.New("secret database detail")}, s.at("a.test", "/"))
	if outcome.Status != models.StatusFailed || outcome.ErrorCode != models.ErrorInternal {
		t.Errorf("outcome = %+v", outcome)
	}
	if outcome.ErrorMessage == "secret database detail" {
		t.Error("the database error reached the check result")
	}
}

func TestProcessMetadataFailureKeepsCheckCompleted(t *testing.T) {
	s := newSite(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<title>T</title>`)
	})
	outcome := process(&fakeResults{metadataErr: errors.New("boom")}, s.at("a.test", "/"))
	if outcome.Status != models.StatusCompleted || outcome.MetadataStatus != models.StageFailed {
		t.Errorf("outcome = %+v", outcome)
	}
}
