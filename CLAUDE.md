# site-checker

A self-hosted Go service that checks website availability on request: it follows redirects, extracts SEO metadata, captures screenshots with headless Chromium, and stores results in MySQL or PostgreSQL. Everything runs in Docker.

**The full requirements are in [docs/SPEC.md](docs/SPEC.md).** Read the relevant sections before working on a component, and follow its phase order (section 13).

## Commands

```bash
docker compose up -d
docker compose down
cd src && go test ./...
cd src && go test -tags=integration ./...
```

`go test ./...` must pass without Docker. Tests that need Docker or a real database use the `integration` build tag and need `POSTGRES_*` and `BROWSER_CDP_URL` in the environment. Database and CDP ports are not published, so run them from inside the Docker network.

Before the first `docker compose up -d`, run `cp .env.example .env` and change the passwords.

## Layout

- `src/`: Go application source, module root, migrations.
- `docker/`: Dockerfiles and database configuration. No application code.
- `storage/screenshots/`: screenshots, mounted from the host. Not committed.
- `docs/`: specification.

## Development Guidelines

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
15. When a requirement changes or a gap is decided, update `docs/SPEC.md` in the same change.

## Workflow

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

## Architectural Decisions

The following decisions must be preserved. Do not change them without discussing the reasons first.

- Go is the primary programming language.
- The application exposes an HTTP REST API.
- The entire infrastructure runs in Docker.
- The API is accessible from the host machine, published on loopback by default.
- Application source code lives in `src/`.
- Docker configurations live in `docker/`.
- PostgreSQL and MySQL are supported database engines.
- Database drivers are interchangeable through a common interface and register themselves with `database.Register`.
- Database connection parameters are separated by engine.
- The `checks` table is the work queue. No Redis or external broker.
- HTTP redirect depth is configurable.
- Redirect chains are recorded.
- Cross-domain redirects are supported.
- Headless Chromium is used for screenshots, in a dedicated container.
- Screenshots are stored persistently.
- Check results are stored in the selected database.
- HTTP availability and screenshot generation are independent, and each stage's result is persisted when the stage ends.
- HTTP 200 is the only status considered available.
- SSRF protection is mandatory: all check traffic, including Chromium's through the egress proxy, uses the guarded dialer.
- Checks run once, on request. Scheduling and alerting are out of scope.
