# Design Decisions

## Why gRPC + gRPC-Web

Using gRPC between frontend and backend. Browsers can't do native gRPC so there's a grpc-web wrapper baked into the Go binary on :8080. Considered Envoy but didn't want another container for what amounts to format translation.

Tradeoff: embedding the proxy means less operational complexity but couples the web layer to the grpc server. Fine for this scale.

## Why Postgres

I needed to implement `EXCLUDE USING gist` on tstzrange for overlap prevention at the DB level. Also need `SELECT FOR UPDATE` for row locking. SQLite can't do either. MySQL doesn't have range exclusion constraints.

- tstzrange handles timezone-aware time math correctly
- exclusion constraints make double booking structurally impossible
- connection pooling scales well horizontally

Tradeoff: heavier setup vs correctness guarantees.

## What Counts as a Conflict

Requirements didn't define this so I made assumptions:
- Same user, overlapping time range = conflict
- Adjacent slots fine (10-11 then 11-12), end is exclusive `[)`
- Different users same time = fine, no shared resource model
- Updating an appointment checks overlaps too, excluding itself

If there were rooms or equipment I would've added a resource table and extended the exclusion constraint.

**Question I would've asked**: do conflicts apply per-user or globally? Are there shared resources like rooms?

## Handling Concurrent Access

Two layers:
1. App-level overlap check before insert
2. DB exclusion constraint catches races

Tested this: 10 goroutines hit the same slot simultaneously. Exactly 1 wins, 9 get `AlreadyExists`. Postgres does the heavy lifting.

Tradeoff: strict consistency over UX. I'd rather reject a valid request than allow a double booking.

## Auth

HTTPOnly cookies. Access token 15min, refresh token 7 days. Refresh rotation — old token revoked on each refresh, reuse of revoked token nukes all tokens for that user (theft detection).

REST endpoints for auth (`/auth/login`, `/auth/register`, `/auth/refresh`, `/auth/logout`). grpc-web uses `withCredentials` for cookie passthrough.

Bcrypt passwords, HS256 with explicit alg check, same error for wrong email/password (no user enumeration), ownership returns 404 not 403.

Rate limiting: IP-based token bucket on login/register. 5 req/s burst 10.

## No ORM

Raw SQL + pgx. 6 tables worth of queries, an ORM adds indirection for no benefit at this size.


---

## Open-Ended Questions

### Recurring Appointments

I didn't implement this but here's how i would've approached it:

Add `recurrence_rule` column using iCalendar RULE format (RFC 5545). Same standard Google Calendar uses. Store only the rule, expand occurrences on read with a time window.

```sql
ALTER TABLE appointments ADD COLUMN rule TEXT;
ALTER TABLE appointments ADD COLUMN recurring_until TIMESTAMPTZ;
```

The tricky part is conflict detection — you would need to expand the recurrence before checking overlaps. For the exclusion constraint you would either:
- Materialize all occurrences as rows (simple but storage heavy)
- Check conflicts in application code against expanded ranges (flexible but slower)

I would go with materialization for the first version.

### Scalability

Current setup: one Go binary + Postgres. Stateless server so horizontal scaling is straightforward.

What changes:
- **pgbouncer** in front of Postgres for connection pooling
- **read replicas** for list queries (appointments are read-heavy)
- **redis** for session/token caching and hot appointment lists
- rate limiter state moves to redis (currently in-memory per instance)

What doesn't change: the grpc contract, handler logic, auth flow. The server is already stateless so load balancing just works.

### Real-Time Updates

Right now the client refetches after mutations. For real-time:
- **server-sent events** on the HTTP server (already running on :8080). Cheaper than websockets for one-directional updates.
- or grpc server streaming for appointment change notifications

I would start with SSE because it's simpler and the update pattern is "server pushes, client receives" — no bidirectional needed.

---

## Assumptions

1. Conflicts are per-user not global (no shared resources)
2. Auth is needed even though spec doesn't require it
3. Soft delete is better than hard delete for appointments
4. Appointment times are timezone-aware (TIMESTAMPTZ)
5. Attendees field exists but invitations aren't implemented yet

## What I'd Do Differently with More Time

1. Recurring appointments (rule based)
2. Attendee invitations with email notifications
3. Structured logging + tracing (right now it's just log.Printf)
4. Integration tests with docker-compose Postgres
5. Calendar view on frontend instead of list view

## Questions I Would've Asked

1. **Conflict scope** — per-user or per-resource? Are there shared rooms/equipment?
2. **Buffer time** — should there be padding between appointments (eg 15min)?
3. **Recurring appointments** — how complex? Daily/weekly only or full rule support?
4. **Multi-timezone** — do users span timezones? Display in local or appointment tz?
5. **Appointment lifecycle** — can users edit past appointments? What's the cancellation policy?
