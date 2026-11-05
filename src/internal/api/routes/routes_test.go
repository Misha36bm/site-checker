package routes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Misha36bm/site-checker/src/internal/checker"
	"github.com/Misha36bm/site-checker/src/internal/models"
	"github.com/Misha36bm/site-checker/src/internal/netguard"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

type fakeChecks struct {
	check *models.Check
	err   error
	panic bool
}

func (f fakeChecks) Create(_ context.Context, url string) (*models.Check, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &models.Check{ID: 7, URL: url, Status: models.StatusPending}, nil
}

func (f fakeChecks) Get(context.Context, int64) (*models.Check, error) {
	if f.panic {
		panic("secret internal detail")
	}
	return f.check, f.err
}

func serve(deps Deps, method, target, body string) *httptest.ResponseRecorder {
	if deps.DB == nil {
		deps.DB = fakePinger{}
	}
	if deps.Checks == nil {
		deps.Checks = fakeChecks{}
	}
	if deps.MaxBodyBytes == 0 {
		deps.MaxBodyBytes = 256
	}
	rec := httptest.NewRecorder()
	New(deps).ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader(body)))
	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct{ Code, Message string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %q", rec.Body)
	}
	if body.Error.Message == "" {
		t.Errorf("error has no message: %s", rec.Body)
	}
	return body.Error.Code
}

func TestHealth(t *testing.T) {
	cases := []struct {
		name     string
		pingErr  error
		code     int
		status   string
		database string
	}{
		{"database reachable", nil, http.StatusOK, "ok", "ok"},
		{"database down", errors.New("dial tcp 10.1.2.3:5432: refused"), http.StatusServiceUnavailable, "unavailable", "unreachable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(Deps{DB: fakePinger{tc.pingErr}}, http.MethodGet, "/health", "")

			if rec.Code != tc.code {
				t.Errorf("code = %d, want %d", rec.Code, tc.code)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q", got)
			}
			if strings.Contains(rec.Body.String(), "10.1.2.3") {
				t.Errorf("response exposes an internal address: %s", rec.Body)
			}
			var body struct{ Status, Database string }
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Status != tc.status || body.Database != tc.database {
				t.Errorf("body = %+v", body)
			}
		})
	}
}

func TestCreateCheckAccepted(t *testing.T) {
	rec := serve(Deps{}, http.MethodPost, "/api/v1/checks", `{"url":"https://example.com"}`)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body)
	}
	want := `{"id":7,"status":"pending","url":"https://example.com"}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func TestCreateCheckErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
		err  error
		code int
		want string
	}{
		{"malformed JSON", `{"url":`, nil, 400, "invalid_request"},
		{"empty body", ``, nil, 400, "invalid_request"},
		{"wrong type", `{"url":5}`, nil, 400, "invalid_request"},
		{"unknown field", `{"url":"https://a.com","x":1}`, nil, 400, "invalid_request"},
		{"two objects", `{"url":"https://a.com"}{}`, nil, 400, "invalid_request"},
		{"too large", `{"url":"` + strings.Repeat("a", 300) + `"}`, nil, 413, "payload_too_large"},
		{"invalid URL", `{"url":"ftp://a.com"}`, &netguard.InvalidURLError{Reason: "bad scheme"}, 400, "invalid_url"},
		{"queue full", `{"url":"https://a.com"}`, checker.ErrQueueFull, 503, "queue_full"},
		{"store failure", `{"url":"https://a.com"}`, errors.New("pq: secret detail"), 500, "internal_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(Deps{Checks: fakeChecks{err: tc.err}}, http.MethodPost, "/api/v1/checks", tc.body)
			if rec.Code != tc.code {
				t.Fatalf("code = %d, want %d, body = %s", rec.Code, tc.code, rec.Body)
			}
			if got := errorCode(t, rec); got != tc.want {
				t.Errorf("error code = %q, want %q", got, tc.want)
			}
			if strings.Contains(rec.Body.String(), "secret") {
				t.Errorf("response exposes internals: %s", rec.Body)
			}
		})
	}
}

func TestQueueFullSendsRetryAfter(t *testing.T) {
	rec := serve(Deps{Checks: fakeChecks{err: checker.ErrQueueFull}}, http.MethodPost, "/api/v1/checks", `{"url":"https://a.com"}`)
	if rec.Header().Get("Retry-After") == "" {
		t.Error("Retry-After is missing")
	}
}

func TestGetCheck(t *testing.T) {
	path := "/app/storage/screenshots/1.png"
	check := &models.Check{
		ID:               1,
		URL:              "https://example.com",
		Status:           models.StatusCompleted,
		MetadataStatus:   models.StageSkipped,
		ScreenshotStatus: models.StageSkipped,
		ScreenshotPath:   &path,
		Attempts:         2,
	}
	rec := serve(Deps{Checks: fakeChecks{check: check}}, http.MethodGet, "/api/v1/checks/1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "completed" || body["metadata_status"] != "skipped" {
		t.Errorf("body = %v", body)
	}
	for _, field := range []string{"screenshot_path", "attempts", "locked_until", "url_hash", "is_available", "final_url"} {
		if _, present := body[field]; present {
			t.Errorf("response must not include %s: %s", field, rec.Body)
		}
	}
}

func TestGetCheckErrors(t *testing.T) {
	cases := []struct {
		name   string
		target string
		err    error
		code   int
		want   string
	}{
		{"not a number", "/api/v1/checks/abc", nil, 400, "invalid_request"},
		{"zero", "/api/v1/checks/0", nil, 400, "invalid_request"},
		{"negative", "/api/v1/checks/-1", nil, 400, "invalid_request"},
		{"overflow", "/api/v1/checks/99999999999999999999", nil, 400, "invalid_request"},
		{"missing", "/api/v1/checks/5", checker.ErrCheckNotFound, 404, "not_found"},
		{"store failure", "/api/v1/checks/5", errors.New("secret detail"), 500, "internal_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(Deps{Checks: fakeChecks{err: tc.err}}, http.MethodGet, tc.target, "")
			if rec.Code != tc.code {
				t.Fatalf("code = %d, want %d", rec.Code, tc.code)
			}
			if got := errorCode(t, rec); got != tc.want {
				t.Errorf("error code = %q, want %q", got, tc.want)
			}
			if strings.Contains(rec.Body.String(), "secret") {
				t.Errorf("response exposes internals: %s", rec.Body)
			}
		})
	}
}

func TestRoutingErrorsUseJSONFormat(t *testing.T) {
	cases := []struct {
		name, method, target string
		code                 int
		want                 string
	}{
		{"unknown path", http.MethodGet, "/nope", 404, "not_found"},
		{"wrong method on health", http.MethodPost, "/health", 405, "method_not_allowed"},
		{"wrong method on checks", http.MethodDelete, "/api/v1/checks", 405, "method_not_allowed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(Deps{}, tc.method, tc.target, "")
			if rec.Code != tc.code {
				t.Fatalf("code = %d, want %d", rec.Code, tc.code)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q", got)
			}
			if got := errorCode(t, rec); got != tc.want {
				t.Errorf("error code = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMethodNotAllowedKeepsAllowHeader(t *testing.T) {
	rec := serve(Deps{}, http.MethodDelete, "/api/v1/checks", "")
	if !strings.Contains(rec.Header().Get("Allow"), http.MethodPost) {
		t.Errorf("Allow = %q", rec.Header().Get("Allow"))
	}
}

func TestPanicBecomesInternalError(t *testing.T) {
	rec := serve(Deps{Checks: fakeChecks{panic: true}}, http.MethodGet, "/api/v1/checks/1", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d", rec.Code)
	}
	if got := errorCode(t, rec); got != "internal_error" {
		t.Errorf("error code = %q", got)
	}
	if strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "goroutine") {
		t.Errorf("response exposes internals: %s", rec.Body)
	}
}
