# site-checker

## 1. Project Overview

**Project name:** site-checker

**Purpose:** A self-hosted website monitoring and inspection service.

The application checks website availability, follows HTTP redirects, captures webpage screenshots, extracts SEO metadata, and stores check results in a database.

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

### Configuration

```env
HTTP_MAX_REDIRECTS=10
```

The value represents the maximum number of redirect transitions allowed.

If the limit is exceeded, the check must finish with an appropriate error status.

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
  "original_url": "https://example.com",
  "final_url": "https://new-domain.com",
  "http_status_code": 200,
  "is_available": true,
  "redirect_count": 2,
  "redirect_chain": [
    {
      "url": "https://example.com",
      "status_code": 301
    },
    {
      "url": "https://www.example.com",
      "status_code": 302
    },
    {
      "url": "https://new-domain.com",
      "status_code": 200
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
DB_DRIVER=mysql

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

- `database.go`: database initialization and connection lifecycle.
- `config.go`: database configuration and driver selection.
- `interfaces.go`: common database interfaces.
- `mysql/`: MySQL implementation.
- `postgres/`: PostgreSQL implementation.

Each database driver must implement the required repository contracts.

Database migrations must be managed separately for each supported engine when SQL dialect differences require it.

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
- Services communicate through a Docker network.
- API ports are exposed to the host machine.
- Screenshots use persistent Docker volumes.
- Database data uses persistent Docker volumes.
- Configuration is passed through environment variables.
- Containers should support automatic restart policies.
- Application logs should be accessible through Docker logs.

### Host access

The API must listen on `0.0.0.0` inside its container.

Example:

```yaml
ports:
  - "8080:8080"
```

The API should then be accessible from the host machine:

```text
http://localhost:8080
```

A custom hostname may also be configured through the host machine's DNS or hosts file.

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

This structure is a starting point. Keep Docker infrastructure in `docker/`, application source code in `src/`, and persistent application data in `storage/`.

Do not put application source code or Go business logic inside Docker configuration directories.

Use Docker volumes for persistent data.

The API container should build its Go application from `src/`.

Chromium may run in a dedicated container or alongside the API, depending on the chosen browser automation architecture. The decision should consider isolation, resource consumption, and operational complexity.

## 7. API Requirements

Base path:

`/api/v1`

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

### Get Check

`GET /api/v1/checks/{id}`

Return the current check status and all available results.

### List Checks

`GET /api/v1/checks`

Support:

- Pagination.
- URL filtering.
- Status filtering.
- Sorting by creation date.

### Get Screenshot

`GET /api/v1/checks/{id}/screenshot`

Return the stored PNG screenshot.

### Health Check

`GET /health`

Return application health and database connectivity status.

## 8. Database Model

The primary `checks` entity should contain:

| Field | Type | Description |
|---|---|---|
| id | BIGINT | Primary key |
| url | TEXT | Original URL |
| final_url | TEXT | Final destination |
| status | VARCHAR | Processing status |
| http_status_code | INTEGER | Final HTTP status |
| is_available | BOOLEAN | HTTP 200 result |
| response_time_ms | BIGINT | HTTP response time |
| redirect_count | INTEGER | Number of redirects |
| redirect_chain | JSON | Redirect history |
| title | TEXT | Page title |
| description | TEXT | Meta description |
| screenshot_path | TEXT | Screenshot storage key |
| error_message | TEXT | Error details |
| created_at | TIMESTAMP | Creation time |
| completed_at | TIMESTAMP | Completion time |

Use database-compatible types and migrations for each engine.

Keep database-specific implementation details inside the database layer.

## 9. Processing Workflow

1. Client submits a URL.
2. API validates the input.
3. Database creates a pending check record.
4. Background worker picks up the task.
5. HTTP checker performs the request.
6. Redirects are followed according to the configured depth.
7. Redirect chain and final response are recorded.
8. Metadata is extracted.
9. Chromium captures a screenshot.
10. Results are persisted.
11. The check status is updated.

Use a controlled worker pool.

The API must not block until the entire check finishes.

Do not introduce Redis or an external message broker without a clear requirement.

## 10. Security Requirements

### SSRF Protection

- Allow only HTTP and HTTPS.
- Reject localhost and loopback addresses.
- Reject private and internal IP ranges.
- Reject link-local and multicast addresses.
- Validate DNS resolution.
- Protect against DNS rebinding.
- Revalidate every redirect destination.
- Block cloud metadata endpoints.
- Restrict Chromium navigation and subresource requests.
- Do not allow redirects to bypass security validation.

### Resource Limits

- Configurable HTTP timeout.
- Configurable browser timeout.
- Configurable redirect depth.
- Maximum concurrent checks.
- Maximum downloaded response size.
- Maximum screenshot dimensions and file size.
- Browser resource limits.
- Cleanup of temporary files.

### API Security

- Validate request payloads.
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

DB_DRIVER=mysql

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

HTTP_TIMEOUT=15s
HTTP_MAX_REDIRECTS=10

BROWSER_TIMEOUT=30s
BROWSER_VIEWPORT_WIDTH=1366
BROWSER_VIEWPORT_HEIGHT=768

MAX_CONCURRENT_CHECKS=5

SCREENSHOT_STORAGE_PATH=/app/storage/screenshots
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

Use mocks for unit tests where appropriate.

Run:

```bash
cd src && go test ./...
```

## 13. Development Guidelines for Claude

When working on the project:

1. Inspect existing code before making changes.
2. Follow the established directory structure.
3. Keep Docker configuration separate from application source code.
4. Do not couple business logic to a particular database engine.
5. Keep database-specific SQL inside the corresponding driver implementation.
6. Do not introduce unnecessary dependencies.
7. Use idiomatic Go.
8. Add tests for new functionality.
9. Preserve existing Docker volumes and data.
10. Do not hardcode credentials or environment-specific settings.
11. Follow SSRF protection requirements.
12. Keep API and background processing independent.
13. Explain significant architectural changes before implementation.
14. Do not rewrite working components without a clear reason.

### Development workflow

For each task:

1. Understand the requirement.
2. Inspect the relevant components.
3. Propose a concise implementation plan.
4. Implement changes incrementally.
5. Add or update tests.
6. Run tests.
7. Verify Docker integration when relevant.
8. Summarize changes and remaining issues.

Do not generate the entire application in one step.

## 14. Initial Development Priorities

### Phase 1 — Infrastructure

- Create the project structure.
- Initialize Go module inside `src/`.
- Create Dockerfiles.
- Configure Docker Compose.
- Configure API networking.
- Set up PostgreSQL and MySQL configurations.
- Implement environment-based database driver selection.
- Set up database migrations.

### Phase 2 — Database Layer

- Define repository interfaces.
- Implement MySQL driver.
- Implement PostgreSQL driver.
- Implement connection pooling.
- Implement migrations.
- Add database tests.

### Phase 3 — HTTP Checker

- Implement URL validation.
- Implement HTTP requests.
- Implement configurable redirect depth.
- Implement redirect chain recording.
- Implement response time measurement.
- Implement SSRF protection.

### Phase 4 — Metadata and Browser

- Implement metadata extraction.
- Integrate headless Chromium.
- Implement screenshot generation.
- Configure browser resource limits.
- Implement persistent screenshot storage.

### Phase 5 — API and Background Processing

- Implement REST endpoints.
- Implement check creation.
- Implement worker pool.
- Implement result retrieval.
- Implement pagination and filtering.
- Implement screenshot access.
- Implement health endpoint.

### Phase 6 — Finalization

- Add integration tests.
- Verify both database engines.
- Verify Docker networking.
- Verify persistent volumes.
- Review security.
- Update documentation.

## 15. Architectural Decisions

The following decisions must be preserved:

- Go is the primary programming language.
- The application exposes an HTTP REST API.
- The entire infrastructure runs in Docker.
- The API is accessible from the host machine.
- Application source code lives in `src/`.
- Docker configurations live in `docker/`.
- PostgreSQL and MySQL are supported database engines.
- Database drivers are interchangeable through a common interface.
- Database connection parameters are separated by engine.
- HTTP redirect depth is configurable.
- Redirect chains are recorded.
- Cross-domain redirects are supported.
- Headless Chromium is used for screenshots.
- Screenshots are stored persistently.
- Check results are stored in the selected database.
- HTTP availability and screenshot generation are independent.
- HTTP 200 is the only status considered available.
- SSRF protection is mandatory.

Do not change these decisions without discussing the reasons first.