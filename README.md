# schedule-management-api

go + postgres + grpc. scheduling appointments with overlap prevention.

## quick start

### option 1: docker compose

```bash
docker compose up --build
```

api on `:50051` (grpc) and `:8080` (grpc-web for browser).

### option 2: local

need postgres running somewhere.

```bash
cp .env.example .env   # fill in your db url + jwt secret
go run ./cmd/server
```

auto-runs migration on startup. output looks like:
```
connected to postgres
migration applied
grpc server on :50051
grpc-web proxy on :8080
```

frontend talks to `:8080`.

## api

single service, `ScheduleService`:

- `Register` / `Login` — sets httponly cookies (access + refresh token)
- `CreateAppointment` / `GetAppointment` / `ListAppointments` / `UpdateAppointment` / `DeleteAppointment`

auth endpoints are REST (`/auth/login`, `/auth/register`, `/auth/refresh`, `/auth/logout`). everything else is grpc-web.

grpc-web wrapper is built into the binary, no envoy needed.

## overlap prevention

two layers:

1. app checks for overlaps before inserting
2. postgres `EXCLUDE USING gist` constraint catches race conditions

10 concurrent goroutines booking the same slot — 1 wins, 9 rejected. tested.

## tests

```bash
go test -v ./internal/handler/ -count=1
```

4 tests: register/login, crud, concurrent booking (10 goroutines), ownership check (IDOR).

needs a running postgres with `DATABASE_URL` and `JWT_SECRET` set.
