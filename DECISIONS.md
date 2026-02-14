# decisions

## grpc + grpc-web

grpc between frontend and backend. browsers cant do native grpc so theres a grpc-web wrapper baked into the go binary on :8080. considered envoy but didnt want another container for what amounts to format translation. also looked at connect-go but stuck with grpc since thats what was asked.

tradeoff: embedding the proxy means less operational complexity but couples the web layer to the grpc server. fine for this scale.

## data persistence (postgres)

need `EXCLUDE USING gist` on tstzrange for overlap prevention at the db level. also need `SELECT FOR UPDATE` for row locking. sqlite cant do either. mysql doesnt have range exclusion constraints.

postgres is overkill for a toy app but the right call for scheduling because:
- tstzrange handles timezone-aware time math correctly
- exclusion constraints make double booking structurally impossible
- connection pooling scales well horizontally

tradeoff: heavier setup vs correctness guarantees. worth it.

## what counts as a conflict

stakeholders didnt define this so i made assumptions:

- same user, overlapping time range = conflict
- adjacent slots fine (10-11 then 11-12), end is exclusive `[)`
- different users same time = fine, no shared resource model
- updating an appointment checks overlaps too, excluding itself

if there were rooms or equipment id add a resource table and extend the exclusion constraint. requirements dont mention shared resources so i left it out.

**question i would have asked**: do conflicts apply per-user or globally? are there shared resources like rooms? does "conflict" include buffer time between appointments?

i proceeded assuming per-user since the requirements say "users schedule appointments" not "users book resources."

## concurrent access

two layers:

1. app-level overlap check before insert
2. db exclusion constraint catches races

tested this: 10 goroutines hit the same slot simultaneously. exactly 1 wins, 9 get `AlreadyExists`. postgres does the heavy lifting.

tradeoff: strict consistency over user experience. id rather reject a valid request than allow a double booking. for a scheduling system this is the right call — the cost of a conflict is higher than the cost of a retry.

## auth

httponly cookies. access token 15min, refresh token 7 days. refresh rotation — old token revoked on each refresh, reuse of revoked token nukes all tokens for that user (theft detection). no jwt in localStorage.

rest endpoints for auth (`/auth/login`, `/auth/register`, `/auth/refresh`, `/auth/logout`). grpc-web uses `withCredentials` for cookie passthrough.

bcrypt passwords, HS256 with explicit alg check, same error for wrong email/password (no user enumeration), ownership returns 404 not 403.

not required by the spec but a multi-user scheduling system without auth is a toy.

## rate limiting

ip-based token bucket on login/register. 5 req/s burst 10. middleware level, runs before auth check.

## no orm

raw sql + pgx. 6 tables worth of queries, an orm adds indirection for no benefit at this size.

## layout

handler/ not controller/, store/ not repository/, no services layer. business logic lives in handlers because theyre all under 50 lines. id extract a service layer if they grew.

---

## open-ended: recurring appointments

didnt implement but heres how id approach it:

add a `recurrence_rule` column to appointments using iCalendar RRULE format (RFC 5545). same standard google calendar uses. store only the rule, expand occurrences on read with a time window.

schema change:
```sql
ALTER TABLE appointments ADD COLUMN rrule TEXT; -- eg "FREQ=WEEKLY;BYDAY=MO,WE,FR"
ALTER TABLE appointments ADD COLUMN recurring_until TIMESTAMPTZ;
```

the tricky part is conflict detection — youd need to expand the recurrence before checking overlaps. for the exclusion constraint youd either:
- materialize all occurrences as rows (simple but storage heavy)
- check conflicts in application code against expanded ranges (flexible but slower)

id go with materialization for the first version. simpler to reason about.

## open-ended: scalability

current setup: one go binary + postgres. stateless server so horizontal scaling is straightforward.

what changes:
- **pgbouncer** in front of postgres for connection pooling
- **read replicas** for list queries (appointments are read-heavy)
- **redis** for session/token caching and hot appointment lists
- rate limiter state moves to redis (currently in-memory per instance)
- migration from `EXCLUDE USING gist` to distributed locking if postgres becomes the bottleneck (unlikely until very high write volume)

what doesnt change: the grpc contract, handler logic, auth flow. the server is already stateless so load balancing just works.

## open-ended: real-time updates

right now the client refetches after mutations. for real-time:

- **server-sent events** on the http server (already running on :8080). cheaper than websockets for one-directional updates.
- or grpc server streaming for appointment change notifications
- frontend subscribes on mount, receives events when appointments in your range change

id start with SSE because its simpler and the update pattern is "server pushes, client receives" — no bidirectional needed.

---

## assumptions

1. conflicts are per-user not global (no shared resources)
2. auth is needed even though spec doesnt require it
3. soft delete is better than hard delete for appointments
4. appointment times are timezone-aware (TIMESTAMPTZ)
5. attendees field exists but invitations arent implemented yet

## what id do differently with more time

1. recurring appointments (rrule based, described above)
2. attendee invitations with email notifications
3. structured logging + tracing (right now its just log.Printf)
4. proper integration tests with docker-compose postgres
5. calendar view on frontend instead of list view

## stakeholder questions i would have asked

1. **conflict scope** — per-user or per-resource? are there shared rooms/equipment?
2. **conflict buffer** — should there be padding between appointments (eg 15min)?
3. **recurring appointments** — how complex? daily/weekly only or full rrule support?
4. **multi-timezone** — do users span timezones? display in local or appointment tz?
5. **appointment lifecycle** — can users edit past appointments? whats the cancellation policy?

proceeded by assuming the simplest reasonable interpretation for each.