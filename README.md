# Simple Bank

A backend web service for a simple bank, built with Go. Based on the [Backend Master Class](https://bit.ly/backendmaster) course by [TECH SCHOOL](https://bit.ly/m/techschool) (also on [Udemy](https://bit.ly/backendudemy)).

The server exposes both **gRPC** (port 9090) and **HTTP** (port 8080, via grpc-gateway) APIs. A minimal **Vue 3** frontend (port 3000) provides a login UI.

## Available APIs

| Method  | Path              | Description                                      | Auth required |
|---------|-------------------|--------------------------------------------------|---------------|
| `POST`  | `/v1/create_user` | Register a new user                              | No            |
| `POST`  | `/v1/login_user`  | Login, returns access and refresh tokens         | No            |
| `PATCH` | `/v1/update_user` | Update user profile                              | Yes           |
| `GET`   | `/v1/verify_email` | Verify email address via link sent after signup  | No            |

Swagger documentation is served at `/swagger/` when the server is running.

The same endpoints are available as gRPC methods on port 9090 (service `pb.SimpleBank`).

> **Note:** The database layer includes schemas for accounts, entries, and transfers, but these are not yet exposed as API endpoints.

## Running the service

### With Docker (recommended)

Requires [Docker](https://www.docker.com/products/docker-desktop) only.

```bash
docker compose up --build
```

This starts all services:

| Service    | URL                     |
|------------|-------------------------|
| HTTP API   | http://localhost:8080   |
| gRPC API   | localhost:9090          |
| Swagger UI | http://localhost:8080/swagger/ |
| Frontend   | http://localhost:3000   |

Database migrations run automatically on startup.

To stop everything:

```bash
docker compose down
```

### Without Docker

Requires [Go](https://golang.org/), [PostgreSQL](https://www.postgresql.org/), [Redis](https://redis.io/), and [golang-migrate](https://github.com/golang-migrate/migrate/tree/master/cmd/migrate) installed locally.

1. Start Postgres and create the database:

```bash
make postgres
make createdb
make migrateup
```

2. Start Redis:

```bash
make redis
```

3. Start the backend server:

```bash
make server
```

4. Start the frontend (in a separate terminal):

```bash
cd frontend
npm install
npm run dev
```

## Running tests

### With Docker (recommended)

Spins up Postgres in a container, runs migrations, executes all tests, and tears everything down:

```bash
make dockertest
```

To run a specific test, start the test dependencies first:

```bash
docker compose -f docker-compose.test.yaml run --rm migrate
go test -v ./db/sqlc/ -run TestCreateUser
docker compose -f docker-compose.test.yaml down
```

### Without Docker

Requires Postgres running locally with the `simple_bank` database created and migrations applied:

```bash
make test
```

## Development

### Code generation

These commands regenerate code from source definitions. You only need them if you modify the corresponding source files.

Generate SQL CRUD code from queries (requires [sqlc](https://github.com/kyleconroy/sqlc)):

```bash
make sqlc
```

Generate gRPC/gateway Go code from proto files (requires `protoc` with Go plugins):

```bash
make proto
```

Generate DB mocks for testing (requires [gomock](https://github.com/golang/mock)):

```bash
make mock
```

### Database migrations

Create a new migration file:

```bash
make new_migration name=<migration_name>
```

Run migrations up or down (one version at a time):

```bash
make migrateup1
make migratedown1
```

### Database documentation

Generate and publish DB docs (requires [dbdocs](https://dbdocs.io/docs) and [DBML CLI](https://www.dbml.org/cli/)):

```bash
make db_docs
make db_schema
```

View the published documentation at [dbdocs.io/techschool.guru/simple_bank](https://dbdocs.io/techschool.guru/simple_bank) (password: `secret`).

### gRPC client

Connect to the gRPC server interactively using [Evans](https://github.com/ktr0731/evans):

```bash
make evans
```

## Configuration

The server reads configuration from `app.env` (and environment variables, which take precedence). Key settings:

| Variable               | Default                          | Description                     |
|------------------------|----------------------------------|---------------------------------|
| `DB_SOURCE`            | `postgresql://root:secret@localhost:5432/simple_bank?sslmode=disable` | Postgres connection string |
| `HTTP_SERVER_ADDRESS`  | `0.0.0.0:8080`                   | HTTP gateway listen address     |
| `GRPC_SERVER_ADDRESS`  | `0.0.0.0:9090`                   | gRPC server listen address      |
| `REDIS_ADDRESS`        | `0.0.0.0:6379`                   | Redis address for async workers |
| `TOKEN_SYMMETRIC_KEY`  | *(set in app.env)*               | 32-byte key for PASETO tokens   |
| `ACCESS_TOKEN_DURATION`| `1m`                             | Access token lifetime           |
| `REFRESH_TOKEN_DURATION`| `24h`                           | Refresh token lifetime          |
| `ALLOWED_ORIGINS`      | `http://localhost:3000`          | CORS allowed origins            |
