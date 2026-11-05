# site-checker: Specification

Product and technical requirements. Working rules and fixed architectural decisions live in `CLAUDE.md`.

## 1. Project Overview

**Project name:** site-checker

**Purpose:** A self-hosted website monitoring and inspection service.

The application checks website availability, follows HTTP redirects, captures webpage screenshots, extracts SEO metadata, and stores check results in a database.

**Scope:** checks run once, on request. Scheduled re-checks and alerting are out of scope for now.

The entire application infrastructure must run inside Docker.

The API must be accessible from the host machine through a locally exposed port or a configured hostname.

## 2. Technology Stack

- **Language:** Go (Golang).
- **API:** HTTP REST API.
- **Database:** Pluggable database engine architecture.
- **Supported database engines:** MySQL, PostgreSQL.
- **Browser automation:** Headless Chromium.
- **Containerization:** Docker and Docker Compose.
- **Data format:** JSON.
- **Screenshot format:** PNG.

The architecture must allow additional database engines to be added in the future without rewriting business logic.

## 3. Core Functionality

For every submitted URL, the application must:

1. Validate the URL.
2. Perform an HTTP availability check.
3. Follow redirects according to the configured redirect depth.
4. Record every redirect in the redirect chain.
5. Record the final URL.
6. Determine whether the final HTTP response is 200 OK.
7. Measure HTTP response time.
8. Capture a screenshot using headless Chromium.
9. Extract the page title.
10. Extract the meta description.
11. Store all results in the selected database engine.

HTTP checking, metadata extraction, and screenshot generation must be independent processing stages.

A screenshot failure must not invalidate a successful HTTP availability check.

### Stage behavior

- **HTTP check:** uses GET and verifies TLS certificates. A certificate error ends the check with `tls_error`.
- **Metadata:** extracted from the raw HTML of the final response, for HTML responses only. Title and description are decoded to UTF-8 using the declared charset. If either value is empty and the screenshot stage rendered the page, it is filled from the rendered DOM, so client-rendered pages are covered.
- **Screenshot:** captured for any final HTML response, including 404 and 500, because an error page is useful evidence. Skipped when no final response was received or the response is not HTML. Captures the viewport only, after the `load` event plus `BROWSER_SETTLE_DELAY`, all within `BROWSER_TIMEOUT`.
- **Screenshot reuse:** a check of an available page (HTTP 200) reuses an existing screenshot of that URL if it is younger than `SCREENSHOT_MAX_AGE`, and Chromium does not run. `browser_final_url` and DOM metadata then stay empty for that check, and `screenshot_captured_at` shows the age of what it references. The HTTP and metadata stages always run. Any other status is always captured and stored separately, so a temporary error never replaces or stands in for the working page.
- **Screenshot file names:** the unpadded base64url SHA-256 of the normalized URL, such as `DxFdsGK3wN0DCxaHjJnepcNUtJ3DezjriEYXnHeD6dc.png`, with the status added for anything but 200, such as `DxFdsGK3wN0DCxaHjJnepcNUtJ3DezjriEYXnHeD6dc-500.png`. Normalization only changes what cannot alter the server's response: scheme and host case, a host's trailing dot, default ports, an empty path, and the fragment.
- The result of each stage is persisted as soon as the stage ends, so a browser crash cannot lose the HTTP result.

## 4. Redirect Handling

Redirect handling is an important part of the application.

The service must support configurable redirect depth.

### Requirements

- Follow HTTP redirects automatically.
- Support 301, 302, 303, 307, and 308.
- Support redirects to different domains.
- Record every URL in the redirect chain.
- Record the HTTP status code of each redirect.
- Record the final destination URL.
- Allow the maximum number of redirects to be configured.
- Prevent infinite redirect loops.
- Detect repeated URLs in the redirect chain.
- Handle redirect timeouts and invalid destinations.

### Redirect semantics

- `HTTP_MAX_REDIRECTS=0` means no redirect is followed.
- `HTTP_TIMEOUT` covers the whole chain, not each hop.
- `response_time_ms` runs from the start of the first request to the headers of the final response. Each chain entry also records its own `duration_ms`.
- Each check has its own in-memory cookie jar, because some sites redirect back to the same URL after setting a cookie.
- Loop detection compares URLs without fragments. A repeated URL is a loop unless the cookie jar changed since the previous visit. `HTTP_MAX_REDIRECTS` remains the hard limit.
- A 3xx response without a usable `Location` header is treated as an invalid redirect destination.
- `redirect_chain` contains HTTP redirects only. `meta refresh` and JavaScript navigations are not part of it; they count against the redirect limit in the browser, and where the browser ended up is stored in `browser_final_url`.

### Configuration

```env
HTTP_MAX_REDIRECTS=10
```

The value represents the maximum number of redirect transitions allowed.

If the limit is exceeded, the check finishes with status `failed` and error code `redirect_limit`.

### Redirect Chain Example

Initial URL:

`https://example.com`

Redirect chain:

```text
https://example.com
    -> 301
https://www.example.com
    -> 302
https://new-domain.com
    -> 200
```

Expected result:

```json
{
  "url": "https://example.com",
  "final_url": "https://new-domain.com",
  "http_status_code": 200,
  "is_available": true,
  "redirect_count": 2,
  "redirect_chain": [
    {
      "url": "https://example.com",
      "status_code": 301,
      "duration_ms": 84
    },
    {
      "url": "https://www.example.com",
      "status_code": 302,
      "duration_ms": 61
    },
    {
      "url": "https://new-domain.com",
      "status_code": 200,
      "duration_ms": 143
    }
  ]
}
```

The redirect chain must be stored in a structured format, preferably JSON/JSONB where supported, or in a separate relational table.

The application must validate every redirect destination against SSRF protection rules. Redirects must never bypass URL validation.

The configured redirect limit must be respected by both the HTTP checker and browser navigation.

## 5. Database Architecture

The application must support multiple database engines through a pluggable architecture.

### Supported engines

Initial implementations:

- MySQL.
- PostgreSQL.

Future engines should be addable without changing application business logic.

### Database configuration

Database connection settings must be separated by engine.

Do not use one shared set of host, port, username, and password variables for every database engine.

Use an explicit database driver selector and independent connection configuration for each engine.

Example:

```env
DB_DRIVER=postgres

# MySQL configuration
MYSQL_HOST=mysql
MYSQL_PORT=3306
MYSQL_DATABASE=site_checker
MYSQL_USERNAME=site_checker
MYSQL_PASSWORD=secret

# PostgreSQL configuration
POSTGRES_HOST=postgres
POSTGRES_PORT=5432
POSTGRES_DATABASE=site_checker
POSTGRES_USERNAME=site_checker
POSTGRES_PASSWORD=secret
POSTGRES_SSLMODE=disable
```

Only the selected database driver should be initialized.

Do not require credentials for unused database engines.

### Database abstraction

Implement a common repository interface for application operations.

Business logic must not depend on a specific database engine.

Suggested structure:

```text
src/internal/database/
├── database.go
├── config.go
├── interfaces.go
├── mysql/
│   └── mysql.go
└── postgres/
    └── postgres.go
```

Responsibilities:

- `database.go`: driver registry (`Register`, `Open`) and connection lifecycle.
- `config.go`: database configuration and driver selection.
- `interfaces.go`: common database interfaces and shared errors such as `ErrNotFound`.
- `mysql/`: MySQL implementation.
- `postgres/`: PostgreSQL implementation.

`database` must not import its drivers, because the drivers import `database` for its contracts and that would create an import cycle. Each driver registers itself with `database.Register` in `init()`, and `cmd/server` imports the drivers with blank imports.

Each database driver must implement the required repository contracts.

Database migrations must be managed separately for each supported engine when SQL dialect differences require it. They are embedded in the binary with `embed`.

Use parameterized queries.

Do not place SQL queries inside HTTP handlers or business services.

### Database connection management

- Configure connection pool limits.
- Configure connection lifetime.
- Configure connection idle timeout.
- Implement graceful connection closing.
- Handle database connection failures.
- Support database health checks.

## 6. Docker Architecture

Docker is the primary runtime environment.

The complete application infrastructure must be described through Docker and Docker Compose.

The application must be accessible from the host machine.

### Requirements

- API runs inside a Docker container.
- Chromium runs inside Docker.
- Database runs inside Docker or can connect to an external database.
- Services communicate through Docker networks, segmented as described below.
- Database and CDP ports are not published to the host.
- API ports are exposed to the host machine.
- Screenshots are stored in a host folder mounted into the API container, so they can be opened directly on the computer.
- Database data uses persistent Docker volumes.
- Configuration is passed through environment variables.
- Containers should support automatic restart policies.
- Application logs should be accessible through Docker logs.

### Host access

The API must listen on `0.0.0.0` inside its container.

Example:

```yaml
ports:
  - "${API_PUBLISH_HOST:-127.0.0.1}:${API_PUBLISH_PORT:-8080}:${APP_PORT:-8080}"
```

The API is published on the host's loopback interface by default. It can start a browser on request, so exposing it to the local network must be a deliberate choice: set `API_PUBLISH_HOST=0.0.0.0` and configure `API_KEY`.

The API should then be accessible from the host machine:

```text
http://localhost:8080
```

A custom hostname may also be configured through the host machine's DNS or hosts file.

The loopback default applies to the host side of the port mapping only.

Do not bind the API exclusively to `127.0.0.1` inside the container.

### Docker Compose

Use Docker Compose to define and orchestrate application services.

The project should support starting the required infrastructure with:

```bash
docker compose up -d
```

And stopping it with:

```bash
docker compose down
```

Persistent database and screenshot data must survive container recreation.

Database services use Compose profiles, so only the selected engine starts. Set `COMPOSE_PROFILES` to match `DB_DRIVER`. The API declares `depends_on` for both engines with `required: false` and also retries its database connection at startup, which keeps external databases working.

### Network segmentation

| Network | Type | Members | Purpose |
|---|---|---|---|
| `backend` | internal | API, database | Database access |
| `browser` | internal | API, Chromium | CDP and the egress proxy |
| `egress` | regular | API | Outbound checks and the published port |

Chromium has no route to the database or the internet. Its only way out is the proxy described in the SSRF section.

### Project directory structure

The repository must separate application source code from Docker infrastructure.

Use the following proposed structure:

```text
site-checker/
│
├── docker/
│   ├── api/
│   │   └── Dockerfile
│   │
│   ├── browser/
│   │   └── Dockerfile
│   │
│   ├── mysql/
│   │   └── conf.d/
│   │
│   └── postgres/
│       └── conf.d/
│
├── docs/
│   └── SPEC.md
│
├── src/
│   ├── cmd/
│   │   └── server/
│   │       └── main.go
│   │
│   ├── internal/
│   │   ├── api/
│   │   │   ├── handlers/
│   │   │   ├── middleware/
│   │   │   └── routes/
│   │   │
│   │   ├── checker/
│   │   │   ├── http_checker.go
│   │   │   └── service.go
│   │   │
│   │   ├── browser/
│   │   │   ├── browser.go
│   │   │   └── screenshot.go
│   │   │
│   │   ├── metadata/
│   │   │   └── extractor.go
│   │   │
│   │   ├── netguard/
│   │   ├── worker/
│   │   │
│   │   ├── database/
│   │   │   ├── database.go
│   │   │   ├── config.go
│   │   │   ├── interfaces.go
│   │   │   ├── mysql/
│   │   │   └── postgres/
│   │   │
│   │   ├── models/
│   │   └── config/
│   │
│   ├── migrations/
│   │   ├── mysql/
│   │   └── postgres/
│   │
│   ├── tests/
│   ├── go.mod
│   └── go.sum
│
├── storage/
│   └── screenshots/
│
├── docker-compose.yml
├── .env.example
├── .dockerignore
├── .gitignore
└── CLAUDE.md
```

This structure is a starting point. Keep Docker infrastructure in `docker/` and application source code in `src/`. Database data lives in the named Docker volume `postgres_data`. Screenshots live in the host folder `SCREENSHOT_HOST_PATH`, by default `storage/screenshots/`, which is ignored by Git.

Do not put application source code or Go business logic inside Docker configuration directories.

Use Docker volumes for persistent data.

The API container should build its Go application from `src/`.

Chromium runs in a dedicated container, with its own resource limits and no direct network route outside the `browser` network.

Unit tests live next to the code they test. `src/tests/` holds integration and end-to-end tests only.

## 7. API Requirements

Base path:

`/api/v1`

### Conventions

Errors use one format, with no internal paths or stack traces:

```json
{
  "error": {
    "code": "invalid_url",
    "message": "URL scheme must be http or https"
  }
}
```

If `API_KEY` is set, every `/api/v1` request must send `Authorization: Bearer <key>`, or it receives `401`. `/health` stays open. Comparison is constant-time.

Request bodies and URL length are limited by `API_MAX_BODY_BYTES` and `API_MAX_URL_LENGTH`.

### Create Check

`POST /api/v1/checks`

Request:

```json
{
  "url": "https://example.com"
}
```

Response:

```json
{
  "id": 1,
  "status": "pending",
  "url": "https://example.com"
}
```

HTTP status: `202 Accepted`.

Other responses:

- `400`: malformed payload, or a URL that fails static validation (scheme, credentials in the URL, literal blocked IP). DNS-based validation happens in the worker.
- `503` with `Retry-After`: `MAX_PENDING_CHECKS` is reached.

### Get Check

`GET /api/v1/checks/{id}`

Return the current check status and all available results.

### List Checks

`GET /api/v1/checks`

Support:

- Pagination.
- URL filtering (exact match on the original URL).
- Status filtering.
- Sorting by creation date, with `id` as the tie-breaker so pages stay stable.

### Get Screenshot

`GET /api/v1/checks/{id}/screenshot`

Return the stored PNG screenshot.

- `200`: the screenshot exists.
- `404`: the check does not exist, or its screenshot `failed` or was `skipped`. The error code says which.
- `409`: the screenshot stage has not finished yet.

### Health Check

`GET /health`

Return application health and database connectivity status.

## 8. Database Model

The primary `checks` entity should contain:

| Field | Type | Description |
|---|---|---|
| id | BIGINT | Primary key |
| url | TEXT | Original URL |
| url_hash | CHAR(64) | SHA-256 of `url`, indexed for filtering |
| final_url | TEXT | Final destination of the HTTP check |
| browser_final_url | TEXT | Where browser navigation ended |
| status | VARCHAR | Processing status |
| error_code | VARCHAR | Machine-readable error |
| error_message | TEXT | Error details |
| http_status_code | INTEGER | Final HTTP status |
| is_available | BOOLEAN | HTTP 200 result |
| response_time_ms | BIGINT | HTTP response time |
| redirect_count | INTEGER | Number of redirects |
| redirect_chain | JSON | Redirect history |
| metadata_status | VARCHAR | Metadata stage status |
| title | TEXT | Page title |
| description | TEXT | Meta description |
| screenshot_status | VARCHAR | Screenshot stage status |
| screenshot_error | TEXT | Screenshot failure details |
| screenshot_path | TEXT | Screenshot storage key |
| screenshot_captured_at | TIMESTAMP | When the screenshot was taken; can precede the check |
| attempts | INTEGER | Times a worker claimed this check |
| locked_until | TIMESTAMP | Expiry of the worker's claim |
| created_at | TIMESTAMP | Creation time |
| started_at | TIMESTAMP | First claim time |
| completed_at | TIMESTAMP | Completion time |

`url_hash`, `locked_until`, and `attempts` are internal and are not returned by the API.

### Statuses

Check `status`:

- `pending`: waiting for a worker.
- `running`: claimed by a worker.
- `completed`: a final HTTP response was received, whatever its code. A 404 is `completed` with `is_available=false`.
- `failed`: no final HTTP response was received, or the check could not be processed.

A check with a successful HTTP stage is `completed` even if metadata or screenshot failed.

Stage status (`metadata_status`, `screenshot_status`): `pending`, `success`, `failed`, `skipped`.

`error_code` values: `ssrf_blocked`, `invalid_redirect`, `dns_error`, `connection_error`, `tls_error`, `timeout`, `redirect_limit`, `redirect_loop`, `worker_abandoned`, `internal_error`.

When a redirect chain ends in failure, `final_url` and `http_status_code` hold the last URL reached and its status, and `is_available` is `false`.

### Engine-specific types

Use database-compatible types and migrations for each engine. Store all times in UTC.

| Field type | PostgreSQL | MySQL |
|---|---|---|
| Timestamps | `TIMESTAMPTZ` | `DATETIME(3)` (`TIMESTAMP` ends in 2038) |
| Redirect chain | `JSONB` | `JSON` |
| Character set | UTF-8 | `utf8mb4` |

MySQL 8.0 or newer is required for `SKIP LOCKED`.

Keep database-specific implementation details inside the database layer.

## 9. Processing Workflow

1. Client submits a URL.
2. API validates the input.
3. Database creates a pending check record.
4. A background worker claims the check.
5. HTTP checker performs the request.
6. Redirects are followed according to the configured depth.
7. Redirect chain and final response are persisted.
8. Metadata is extracted and persisted.
9. Chromium captures a screenshot, and the result is persisted.
10. The final check status is set.

Use a controlled worker pool.

The API must not block until the entire check finishes.

Do not introduce Redis or an external message broker without a clear requirement.

### Queue

The `checks` table is the queue, so pending work survives restarts.

- Workers claim checks with `SELECT ... FOR UPDATE SKIP LOCKED`, setting `status=running`, `locked_until`, and incrementing `attempts`. This lives behind the repository interface.
- Workers extend `locked_until` while they run.
- `attempts` is also a fencing token: writes must match the claim's attempt number, so a worker whose claim expired cannot overwrite the current worker's results.
- A `running` check with an expired `locked_until` goes back to `pending`. After `CHECK_MAX_ATTEMPTS` claims it becomes `failed` with `worker_abandoned`.
- `MAX_CONCURRENT_CHECKS` limits checks in progress, and `BROWSER_MAX_CONCURRENCY` separately limits screenshot stages, because Chromium uses far more resources.

### Shutdown

On SIGTERM the API stops accepting requests and workers stop claiming checks. In-progress checks get `WORKER_SHUTDOWN_TIMEOUT` to finish; the rest return to `pending`. The database connection closes last.

A check returned to the queue this way does not consume an attempt. `stop_grace_period` in Docker Compose must exceed `WORKER_SHUTDOWN_TIMEOUT`.

### Retention

If `CHECK_RETENTION` is above zero, a background job deletes older checks, and any screenshot file no remaining check references. Several checks can share one file. The default of `0` keeps everything, so monitor disk usage.

## 10. Security Requirements

### SSRF Protection

- Allow only HTTP and HTTPS.
- Reject localhost and loopback addresses.
- Reject private and internal IP ranges.
- Reject link-local and multicast addresses.
- Reject unspecified and reserved ranges, including `0.0.0.0/8`, CGNAT `100.64.0.0/10`, `198.18.0.0/15`, and `240.0.0.0/4`.
- Apply the same rules to IPv6, including IPv4-mapped addresses and NAT64 `64:ff9b::/96`.
- Reject URLs with embedded credentials (`user:pass@host`).
- Validate DNS resolution.
- Protect against DNS rebinding.
- Revalidate every redirect destination.
- Block cloud metadata endpoints.
- Restrict Chromium navigation and subresource requests.
- Do not allow redirects to bypass security validation.

### SSRF enforcement

Checking a URL before connecting is not enough, because a hostname can resolve differently at connect time. All outbound check traffic therefore goes through one guarded dialer in `internal/netguard`. It resolves the name once, refuses the connection if any resolved address is forbidden, dials the resulting IP directly so there is no second lookup to rebind, and validates that IP again in `net.Dialer.Control`, at the moment of connection.

URLs whose last host label is a number or hex value (`2130706433`, `0x7f.1`) are rejected, because browsers read them as IPv4 addresses.

Chromium resolves DNS on its own, and CDP request interception does not see WebSocket or WebRTC traffic, so interception is not a sufficient control. Instead:

- Chromium sits on an internal Docker network with no route out.
- All its traffic goes through a forward proxy in the API process, on a separate listener reachable only from the `browser` network. The proxy uses the guarded dialer.
- Chromium runs with `--proxy-server=...`, `--proxy-bypass-list=<-loopback>` (so loopback also goes through the proxy) and `--force-webrtc-ip-handling-policy=disable_non_proxied_udp`.
- CDP is reachable only from the `browser` network.

The proxy's own 403 reaches the page as an ordinary response, carrying nothing from the destination. Tests of this behavior must check what actually came back: to a no-cors `fetch`, a blocked request looks like success.

Chromium runs with `--no-sandbox`, because its sandbox needs privileges the container deliberately lacks. The container takes that role: unprivileged user, no capabilities, `no-new-privileges`, read-only root filesystem, and the network isolation above.

`SSRF_ALLOWED_CIDRS` lists ranges exempt from blocking, for monitoring internal sites and for end-to-end tests. It is empty by default. Cloud metadata addresses stay blocked regardless.

### Resource Limits

- Configurable HTTP timeout.
- Configurable browser timeout.
- Configurable redirect depth.
- Maximum concurrent checks.
- Maximum pending checks.
- Maximum downloaded response size.
- Maximum screenshot dimensions and file size.
- Browser resource limits.
- Cleanup of temporary files.

### API Security

- Validate request payloads.
- Support optional API key authentication.
- Publish the API on the host's loopback interface by default.
- Use parameterized SQL queries.
- Do not expose credentials.
- Do not expose internal filesystem paths.
- Avoid returning stack traces to API clients.

## 11. Configuration

All application settings must be environment-driven.

Example:

```env
APP_ENV=development
APP_HOST=0.0.0.0
APP_PORT=8080
APP_SHUTDOWN_TIMEOUT=10s

# Host side of the port mapping
API_PUBLISH_HOST=127.0.0.1
API_PUBLISH_PORT=8080

# Optional. Required in practice if the API is published beyond loopback
API_KEY=
API_MAX_BODY_BYTES=4096
API_MAX_URL_LENGTH=2048

# Keep both in sync
DB_DRIVER=postgres
COMPOSE_PROFILES=postgres

MYSQL_HOST=mysql
MYSQL_PORT=3306
MYSQL_DATABASE=site_checker
MYSQL_USERNAME=site_checker
MYSQL_PASSWORD=secret

POSTGRES_HOST=postgres
POSTGRES_PORT=5432
POSTGRES_DATABASE=site_checker
POSTGRES_USERNAME=site_checker
POSTGRES_PASSWORD=secret
POSTGRES_SSLMODE=disable

DB_MAX_OPEN_CONNS=20
DB_MAX_IDLE_CONNS=5
DB_CONN_MAX_LIFETIME=30m
DB_CONN_MAX_IDLE_TIME=5m
DB_CONNECT_TIMEOUT=60s

HTTP_TIMEOUT=15s
HTTP_MAX_REDIRECTS=10
HTTP_MAX_BODY_BYTES=5242880
HTTP_USER_AGENT=site-checker/1.0

SSRF_ALLOWED_CIDRS=

BROWSER_CDP_URL=http://browser:9222
BROWSER_PROXY_LISTEN=0.0.0.0:3128
BROWSER_TIMEOUT=30s
BROWSER_SETTLE_DELAY=500ms
BROWSER_VIEWPORT_WIDTH=1366
BROWSER_VIEWPORT_HEIGHT=768
BROWSER_MAX_CONCURRENCY=2
BROWSER_MEMORY_LIMIT=1g
BROWSER_CPU_LIMIT=1.5
BROWSER_PIDS_LIMIT=512

MAX_CONCURRENT_CHECKS=5
MAX_PENDING_CHECKS=1000
CHECK_LOCK_TTL=2m
CHECK_MAX_ATTEMPTS=3
WORKER_POLL_INTERVAL=1s
WORKER_SHUTDOWN_TIMEOUT=30s

# 0 keeps checks and screenshots forever
CHECK_RETENTION=0

SCREENSHOT_HOST_PATH=./storage/screenshots
# Linux only: the owner of that folder
# APP_UID=1000
# APP_GID=1000
SCREENSHOT_STORAGE_PATH=/app/storage/screenshots
SCREENSHOT_MAX_BYTES=10485760
SCREENSHOT_MAX_AGE=24h
```

Provide `.env.example` with documented defaults.

Do not commit actual credentials.

## 12. Testing Requirements

Use Go's standard testing package.

Test:

- HTTP 200, 301, 302, 404, and 500.
- Redirect chains across multiple domains.
- Configurable redirect depth.
- Redirect loops.
- Invalid redirect destinations.
- DNS and connection errors.
- Database repository implementations.
- Database driver selection.
- Metadata extraction.
- Screenshot generation.
- API endpoints.
- Docker service connectivity.
- Persistent storage across container restarts.
- SSRF rules, including rebinding (a hostname that resolves to a blocked IP at connect time) and blocked redirect targets.
- Browser isolation: a page cannot reach the database, internal addresses, or the network outside the proxy.
- Queue behavior: concurrent claims, recovery of expired claims, graceful shutdown.

The SSRF guard blocks loopback, so `httptest` servers are unreachable by default. The checker therefore takes its resolver and dial policy as dependencies, and tests inject a fake resolver and a permissive policy. Multi-domain redirect tests use the fake resolver too. The default policy has its own direct tests.

One shared contract test suite runs against every database driver.

`go test ./...` must pass without Docker. Tests that need Docker or a real database use the `integration` build tag and read the connection settings from the environment. They run in a temporary database, so existing data is not touched.

Use mocks for unit tests where appropriate.

Run:

```bash
cd src && go test ./...
cd src && go test -tags=integration ./...
```

## 13. Development Phases

Each phase ends with something that runs. PostgreSQL comes first and MySQL follows once the repository interface is stable. Both remain required.

### Phase 1: Skeleton

- Initialize the Git repository and Go module inside `src/`.
- Create the API Dockerfile and Docker Compose with PostgreSQL and network segmentation.
- Implement environment-based configuration and driver selection.
- Implement PostgreSQL connection pooling and embedded migrations.
- Implement `GET /health`.

### Phase 2: End-to-end flow

- Define repository interfaces and the driver registry.
- Implement `POST /api/v1/checks` and `GET /api/v1/checks/{id}`.
- Implement the database-backed queue, worker pool, and graceful shutdown, with a stub checker.

### Phase 3: HTTP checker

- Implement URL validation and the guarded dialer first.
- Implement HTTP requests and response time measurement.
- Implement redirects: depth limit, chain recording, loop detection.
- Implement metadata extraction.

### Phase 4: Browser

- Build a prototype first: the Chromium container, the internal network, and the egress proxy. Verify isolation before integrating.
- Implement screenshot generation and persistent storage.
- Enforce the redirect limit in browser navigation.
- Configure browser resource limits.

### Phase 5: MySQL

- Implement the MySQL driver and migrations.
- Add the Compose profile and configuration.
- Run the shared contract tests on both engines.

### Phase 6: Finalization

- Implement list filtering and pagination, `GET /api/v1/checks/{id}/screenshot`, API key authentication, and retention.
- Add integration tests.
- Verify Docker networking and persistent volumes on both engines.
- Review security.
- Update documentation.
