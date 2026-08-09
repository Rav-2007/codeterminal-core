# Security model — auth, RLS, grants, and the inference hop

Two halves, and they fail in different ways.

**The database** — everything from here down to
[Notes for whoever touches the database next](#notes-for-whoever-touches-the-database-next).
Covers the Supabase posture for `api_keys`, `usage`, the `api_keys_public` view, and the two usage
RPCs. Everything in it was verified live against the production project (`eshpqodurxubjigndiev`) on
**2026-07-17** using an anon key plus a real user JWT — not with `service_role`, which holds
BYPASSRLS and passes every test regardless of whether RLS works. That distinction is the whole
point of this half: **a `service_role` check proves nothing.**

**[The inference hop](#the-inference-hop--zdr-retention-finding)** — the last section. What happens
to a user's prompt after it leaves the proxy. The database half protects key material and row
ownership; **the product's actual privacy promise lives in the inference half**, and it currently
carries an open, corroborated finding: a ZDR-labeled provider retained our prompt content across
requests.

## The model in one paragraph

Two consumers, two postures. The **proxy** (`proxy/main.go`) authenticates to Supabase as
`service_role`, which has BYPASSRLS and its own full grants — it is unaffected by every policy
and grant described here, and none of this can break it. The **browser** (the login site, which
does not exist yet) will authenticate as `anon` before login and `authenticated` after, and for
those roles the tables are locked at two independent layers: RLS policies scope rows to the
caller, and grants restrict which columns and commands are reachable at all. Either layer alone
would deny; both are deliberate.

## Schema

```
api_keys
  id          uuid        primary key
  key_hash    text        sha256 of the raw key -- NEVER exposed to the browser
  key_prefix  text        first 8 chars, for display -- NOT UNIQUE (see Traps)
  label       text
  active      bool
  created_at  timestamptz
  user_id     uuid        nullable, FK -> auth.users(id) ON DELETE RESTRICT
              index: api_keys_user_id_idx

usage
  key_id      uuid        FK -> api_keys.id
  tokens_used bigint
  token_limit bigint
  period_start timestamptz
```

`user_id` is **nullable on purpose.** It fails closed for free: `NULL = auth.uid()` evaluates to
NULL, which is not TRUE, so an unowned key is invisible to every authenticated user without a
line of policy saying so. It must stay nullable until real users exist — `NOT NULL` would force
an invented owner onto the existing rows, which is worse than no owner.

`ON DELETE RESTRICT` is also deliberate, and the reasoning is not obvious. The proxy resolves
keys by `key_hash` alone and **never reads `user_id`**. So `ON DELETE SET NULL` would let you
delete a user and leave their key fully live and working, owned by nobody — a silent hole.
RESTRICT makes deletion fail loudly instead. See trap 4 for the cost of that choice.

## Policies

RLS is ON for both tables. Two policies, both SELECT-only:

```sql
create policy api_keys_select_own
  on public.api_keys for select to authenticated
  using (user_id = (select auth.uid()));

create policy usage_select_own
  on public."usage" for select to authenticated
  using (exists (
    select 1 from public.api_keys k
    where k.id = "usage".key_id and k.user_id = (select auth.uid())
  ));
```

`(select auth.uid())` rather than bare `auth.uid()` so the planner hoists it into an InitPlan
evaluated once instead of per row. Irrelevant at current scale, standard Supabase guidance, free.

**There are no INSERT, UPDATE, or DELETE policies for either table, by design — not by
omission.** Key minting stays proxy-only because the raw key must be generated server-side,
returned once, and never stored; if the browser could INSERT, the browser would be choosing the
key material and the server would store whatever hash it was handed. Usage writes stay
proxy-only because they are the quota mechanism. Both are double-locked: no policy *and* no
grant. Self-service key revocation, when it ships, must go through a `SECURITY DEFINER` function
scoped to the caller's own rows — never an UPDATE grant (see Deferred).

## Grants

| role | api_keys | usage | api_keys_public | reserve_usage / increment_usage |
|---|---|---|---|---|
| `anon` | none | none | none | none |
| `authenticated` | SELECT (id, key_prefix, label, active, created_at, user_id) | SELECT (key_id, tokens_used, token_limit, period_start) | SELECT | none |
| `service_role` | full (unchanged) | full (unchanged) | full (unchanged) | EXECUTE |
| `PUBLIC` | none | none | none | none (explicitly revoked) |

Per-column rather than table-level on both tables. On `api_keys` that hides `key_hash`. On
`usage` no column is sensitive *today* — the reason is that a table-level grant silently exposes
whatever column someone adds next year.

Both RPCs are `SECURITY INVOKER`. Postgres's implicit default for functions is **EXECUTE to
PUBLIC**, which is how `anon` originally had reach to `increment_usage(key_id, -999999)` — a
quota reset. That was latent rather than live (RLS default-deny meant the inner UPDATE hit zero
rows), but it was one permissive policy away from real. EXECUTE is now revoked from PUBLIC,
`anon`, and `authenticated`, with `service_role` explicitly granted.

## The `api_keys_public` view

```sql
-- (id, key_prefix, label, active, created_at, user_id) from api_keys
alter view public.api_keys_public set (security_invoker = true);
```

**Point the frontend at this view, not at `api_keys`.** It excludes `key_hash` structurally, so
`select=*` works against it (see trap 1 for why that matters).

`security_invoker = true` is load-bearing and was **not** set when the view was first created.
Without it a view evaluates RLS as its *owner* (`postgres`), not the caller — it would have
returned all rows to anyone, and because the view is single-table with no aggregates it is
**auto-updatable**, so it would have been a write bypass around both RLS and grants, not just a
read one. The view now has two independent controls: `security_invoker` pushing checks down to a
base table the caller can't reach, and no grant to `anon` at all.

## The four traps

These are the things that will bite a future session. They are all counter-intuitive and none of
them are visible from reading the schema.

### 1. PostgREST's error hint tells you to undo the security model

A denied request returns:

```json
{"code":"42501","hint":"Grant the required privileges to the current role with: GRANT SELECT ON public.api_keys TO authenticated;","message":"permission denied for table api_keys"}
```

That hint is generic and has no idea why the denial happened. **Following it replaces the
per-column grant with a table-level one and re-exposes `key_hash`.**

Whoever writes the frontend will hit this on their first `supabase.from('api_keys').select()`,
because PostgREST passes `*` through, Postgres expands it to every column including `key_hash`,
and the query fails. The fix is never to widen the grant. It is either to enumerate columns
(`.select('id, key_prefix, label, active, created_at')`) or, better, to query `api_keys_public`,
where `select=*` works by construction.

### 2. `grant select (user_id)` looks dead and serves three masters

No client query selects `user_id`. `api_keys_select_own` reads it in its own USING clause and
works fine *without* the grant. Everything about it looks like dead surface an audit should
remove.

**Revoking it silently breaks every usage read.** Verified by controlled experiment on
2026-07-17: with the grant revoked, `GET /usage?select=key_id,tokens_used,token_limit` returned
403; re-granting flipped the identical request back to 200, same user, nothing else changed.

The underlying Postgres behavior is asymmetric and worth knowing:

- A policy's **own USING clause** on its **own table** does *not* re-check column privileges.
  The policy can read a column the caller cannot. (`api_keys_select_own` proves this.)
- A policy's **EXISTS subquery** against **another table** *does*. The subquery introduces a
  separate relation reference into the query's range table, and that one is permission-checked
  normally, columns included.

So `usage_select_own`'s join through `api_keys.user_id` requires the caller to hold
`select (user_id)` on `api_keys`. The grant serves the policy subquery and the view; it serves
no client query. It fails loudly (42501) rather than silently, which is the good failure mode —
but nothing in the grant statement records that a policy depends on it. This paragraph is that
record.

The alternative is moving the join into a `SECURITY DEFINER` helper so the policy runs as its
owner and decouples from the caller's grants. Not done: it trades an invisible coupling for
another SECURITY DEFINER function to get right, and the grant is harmless — a user can only ever
see rows where `user_id` equals their own uid, so it exposes a value they already hold.

### 3. Default privileges — half-fixed, and the catalog lies about the other half

**Status (2026-07-17): tables, views, and sequences are fixed. Functions are not, and cannot be
fixed this way.**

Supabase's stock bootstrap runs, for each of `postgres` and `supabase_admin`:

```sql
alter default privileges in schema public grant all on tables    to anon, authenticated, service_role;
alter default privileges in schema public grant all on functions to anon, authenticated, service_role;
alter default privileges in schema public grant all on sequences to anon, authenticated, service_role;
```

`TABLES` covers views. This is not hypothetical: `api_keys_public` arrived pre-granted SELECT to
`anon` that nobody wrote, and the only reason that wasn't a live hole is that `security_invoker`
(set after the fact) blocked it one layer down.

#### Fixed: tables, views, sequences

```sql
alter default privileges for role postgres in schema public revoke all on tables    from anon, authenticated;
alter default privileges for role postgres in schema public revoke all on sequences from anon, authenticated;
alter default privileges for role postgres in schema public revoke all on functions from anon, authenticated;
```

`for role postgres` is deliberate and is the most misread part of this statement: **the `FOR ROLE`
clause names the role that will *create* the object, not the role running the statement.**
Omitting it silently means `current_user`.

Verified by probe — a table created afterward came back with
`relacl = {postgres=arwdDxtm/postgres,service_role=arwdDxtm/postgres}`. No `anon`, no
`authenticated`, no PUBLIC. `service_role` keeps its own explicit entry in each default ACL, so
the proxy is structurally unaffected by any of this.

#### NOT fixed: functions. And `pg_default_acl` will tell you otherwise.

A fourth statement was applied:

```sql
alter default privileges for role postgres in schema public revoke execute on functions from public;
```

It **did** take. `pg_default_acl` shows no `PUBLIC` grantee under `postgres`/`functions`. The
catalog reads clean.

A function created afterward still arrives with EXECUTE to PUBLIC:

```
proacl = {=X/postgres,postgres=X/postgres,service_role=X/postgres}
          ^^^ bare/empty grantee = PUBLIC
```

Postgres's built-in EXECUTE-to-PUBLIC baseline for functions **is not a row in `pg_default_acl`**.
Removing the explicit entry just falls back to that baseline. The revoke removed the visible
symptom and left the behavior.

> **Do not trust a clean `pg_default_acl` as evidence the function path is closed.** This is a
> nastier trap than the original, because the catalog actively misleads: it shows exactly what a
> correct fix would show, while every new function remains callable by `anon`.

**MECHANISM: UNRESOLVED — do not treat the explanation above as complete.** It contradicts
PostgreSQL's own documentation, which presents
`ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC` as working.
The merge semantics of `get_user_default_acl` were not established, and reconstructing them from
memory was deliberately declined rather than guessed at. One alternative explanation *was* ruled
out: a global (non-schema-scoped) default-ACL entry re-adding PUBLIC — an unfiltered
`pg_default_acl` scan returns zero rows for `defaclnamespace = 0 AND defaclobjtype = 'f'`.
**The probe below is the arbiter — not the catalog, not the docs, not this paragraph.** If a
future session resolves the mechanism, update this section.

##### The probe — the only proof

```sql
create function public.zz_acl_probe_fn() returns int language sql as $$ select 1 $$;
```

```sql
select p.proname, p.proacl
from pg_proc p join pg_namespace n on n.oid = p.pronamespace
where n.nspname = 'public' and p.proname = 'zz_acl_probe_fn';
```

```sql
drop function public.zz_acl_probe_fn();
```

Read `proacl` for a **bare `=X/`**. An ACL entry with an empty grantee is PUBLIC. If it is there,
new functions in `public` are callable by `anon`, whatever `pg_default_acl` says.

#### The rule that replaces the fix

> **Every `SECURITY DEFINER` function in `public` must `REVOKE EXECUTE ... FROM PUBLIC` in the
> same migration that creates it.**

This is a process control, not a database control. There is no known way to make the database
enforce it. It is narrow enough to actually hold, because the severity is **not** uniform across
functions:

- **`SECURITY INVOKER` + EXECUTE-to-PUBLIC is a missing layer, not an open door.** The body runs
  as the *caller*, so the caller's own grants and RLS still apply. This is precisely why the
  pre-revoke `increment_usage` hole was **latent rather than live**: `anon` could call
  `increment_usage(key, -999999)`, and the inner `UPDATE` hit zero rows because RLS was
  default-deny. Worth closing for defense in depth; not an emergency.
- **`SECURITY DEFINER` + EXECUTE-to-PUBLIC is the only control.** The body runs as its *owner*,
  bypassing both the caller's grants and RLS. EXECUTE is the entire perimeter, and it defaults to
  everyone.

Both existing RPCs (`reserve_usage`, `increment_usage`) are `SECURITY INVOKER` and have had
EXECUTE explicitly revoked from PUBLIC — verified live from the browser side (403,
`permission denied for function increment_usage`).

**First place this bites: the deferred self-service key-revocation function** (see Deferred). It
is `SECURITY DEFINER` by necessity, which makes it the first function where forgetting this rule
produces a real hole rather than a missing layer.

#### The `supabase_admin` rows stay, and are correct

`pg_default_acl` still shows three rows set by `supabase_admin` (tables, functions, sequences)
granting to `anon` and `authenticated`. **This is not a half-applied fix.** `defaclrole` names the
role that *creates* the object, and `supabase_admin` does not create objects in `public`.

Evidence — ownership records who created an object:

```sql
select pg_get_userbyid(c.relowner) as owner, c.relkind, count(*) as n
from pg_class c join pg_namespace n on n.oid = c.relnamespace
where n.nspname = 'public' and c.relkind in ('r','v','m','p','f','S')
group by 1, 2 order by 1, 2;
```

```sql
select pg_get_userbyid(p.proowner) as owner, count(*) as n
from pg_proc p join pg_namespace n on n.oid = p.pronamespace
where n.nspname = 'public'
group by 1 order by 1;
```

Everything in `public` is owned by `postgres`, so those entries have never fired. Moot in any
case: `pg_has_role(current_user, 'supabase_admin', 'member')` is **false** — they cannot be
altered from the SQL editor, and shouldn't be even if they could, since that changes the behavior
of a platform role with an unbounded blast radius.

Residual: if Supabase ever ships a platform feature that creates a relation in `public` as
`supabase_admin`, it arrives pre-granted to `anon` and nothing here stops it. Re-run the ownership
query periodically rather than assuming.

#### Inspecting default privileges — use the unfiltered query

```sql
select coalesce(pg_get_userbyid(d.defaclrole), '?') as creating_role,
       coalesce(n.nspname, '<GLOBAL - all schemas>') as scope,
       case d.defaclobjtype when 'r' then 'tables/views' when 'f' then 'functions'
            when 'S' then 'sequences' when 'T' then 'types' when 'n' then 'schemas' end as objtype,
       case when a.grantee = 0 then 'PUBLIC' else pg_get_userbyid(a.grantee) end as grantee,
       string_agg(a.privilege_type, ',' order by a.privilege_type) as privs
from pg_default_acl d
left join pg_namespace n on n.oid = d.defaclnamespace
cross join lateral aclexplode(d.defaclacl) a
group by 1, 2, 3, 4
order by 1, 2, 3, 4;
```

**Do not add `where n.nspname = 'public'`** — that is a trap in its own right. Default-ACL entries
come in two kinds: schema-scoped (`defaclnamespace` = the schema's oid) and **global**
(`defaclnamespace` = 0, from an `ALTER DEFAULT PRIVILEGES` with no `IN SCHEMA`). A global entry
left-joins to a NULL `pg_namespace` row, so an `nspname` filter silently discards exactly the rows
most likely to explain a surprise. `get_user_default_acl` consults both kinds. When that filter
was first used here it hid 51 of 69 rows.

Use `aclexplode` rather than reading raw `defaclacl` — eyeballing
`{postgres=arwdDxtm/postgres,anon=...}` strings is how a missing bare `=X/` slips through.

### 4. Account deletion does not work out of the box

Deleting a user who owns any key fails, because of `ON DELETE RESTRICT`:

```json
{"code":"23503","message":"update or delete on table \"users\" violates foreign key constraint \"api_keys_user_id_fkey\" on table \"api_keys\"","detail":"Key (id)=(...) is still referenced from table \"api_keys\"."}
```

**GoTrue surfaces this as an HTTP 500**, not a 409 — an opaque error a frontend cannot explain.
A "delete my account" button will hit it immediately.

This is the accepted cost of RESTRICT, and it is the right trade: `SET NULL` would let the
deletion succeed and leave a working orphaned key behind, since the proxy never reads `user_id`.
Account deletion must therefore be an explicit flow that reassigns or removes the user's keys
first, then deletes the user.

**Open, untested:** whether GoTrue's failed delete is transactional. A partial delete that strips
`auth.identities` while leaving the user row would be a real hazard. Verify when the deletion
flow is built.

## Verification matrix

Re-run this rather than re-deriving it. **It must be run with an anon key + a real user JWT.**
Running it as `service_role` proves nothing — BYPASSRLS makes every row visible and every check
pass.

### Setup

`auth.users` is currently empty and there is no login site, so a test identity has to be created
through GoTrue. **Never `INSERT` into `auth.users` directly** — that skips password hashing, the
`auth.identities` row, and the `aud`/`role`/confirmation fields, producing a user that looks real
and fails strangely. `mailer_autoconfirm` is `false`, so `/auth/v1/signup` returns no session and
is useless here. Use the admin API:

1. `POST /auth/v1/admin/users` (service_role) — `{"email": "...", "password": "...", "email_confirm": true}`
2. `POST /auth/v1/token?grant_type=password` (anon key) — returns a real JWT; assert `sub` == uid
3. Create a disposable `api_keys` row with `user_id` = uid, plus a `usage` row (service_role)

### Reads — anon key + JWT

| request | expect |
|---|---|
| `GET /api_keys_public?select=*` | 200, **exactly 1 row** (the user's own) |
| `GET /api_keys_public?select=id,key_prefix` | 200, 1 row |
| `GET /api_keys_public?select=key_hash` | 400 `42703 column ... does not exist` |
| `GET /api_keys?select=id,key_prefix,label,active,created_at` | 200, 1 row |
| `GET /api_keys?select=*` | 403 `42501` (key_hash not granted) |
| `GET /api_keys?select=key_hash` | 403 `42501` |
| `GET /usage?select=key_id,tokens_used,token_limit` | 200, 1 row |

More than 1 row from `api_keys_public` means RLS is being bypassed — that is a live hole, stop
and investigate `security_invoker`.

### Writes — anon key + JWT (the view is auto-updatable; these matter)

| request | expect |
|---|---|
| `POST /api_keys` | 403 `42501` |
| `PATCH /api_keys?id=eq.<other user's row>` | 403 `42501` |
| `PATCH /api_keys_public?id=eq.<own row>` | 403 `42501` |
| `PATCH /api_keys_public?id=eq.<other user's row>` | 403 `42501` |
| `DELETE /api_keys_public?id=eq.<own row>` | 403 `42501` |
| `POST /rpc/increment_usage` `{"p_key_id":"<any>","p_tokens":-999999}` | 403 `42501 permission denied for function increment_usage` |

Use idempotent payloads (set a column to its current value) so a failed denial doesn't mutate
anything. Follow with a `service_role` read-back to confirm the target row is unchanged.

### Anon-only — anon key, no JWT

| request | expect |
|---|---|
| `GET /api_keys_public?select=*` | 401 `permission denied for **view** api_keys_public` |
| `GET /api_keys?select=*` | 401 `permission denied for table api_keys` |

**Read the object name in that first error.** If it says `api_keys` (the base table) rather than
`api_keys_public` (the view), `anon` still holds a grant on the view and is only being stopped by
`security_invoker` — one layer, not two. That error text is the observable difference between the
two postures.

### Teardown — order is forced by RESTRICT

1. Null the disposable key's `user_id`, set `active: false`
2. `DELETE /auth/v1/admin/users/{uid}`
3. Verify independently: re-read user (expect 404), re-read key (expect `active: false`,
   `user_id: null`)

Optionally, before step 1, attempt the delete *first* — it should fail with `23503`, which
exercises the FK live rather than trusting the catalog. Note `23503` is raised by both RESTRICT
and NO ACTION and cannot distinguish them; use `confdeltype` from `pg_constraint` for that.

## Proxy impact: none, and here's the proof

Every change described here leaves `service_role` untouched. Verified live on 2026-07-17 after
the RPC EXECUTE revokes, against the deployed proxy, using
`proxy/migrations/`-era tooling: 8 concurrent requests against a 100-token key returned
`{200: 4, 429: 4}` — admission stopped at exactly 4 × 25 = 100 — and `tokens_used` settled at
108, i.e. **not** frozen at the raw 100 reservation.

That second assertion is the one that matters and it is easy to get wrong. `reserve_usage` is
synchronous and on the hot path, so any 200 proves it. `increment_usage` is the fire-and-forget
true-up whose failures are swallowed by design — if it lost EXECUTE, every request would still
return a clean 200 while `tokens_used` silently froze at the reservation, over-billing every key
with no error anywhere. **A 200 alone does not test `increment_usage`.** Always assert that
`tokens_used` moved off the reservation, using a `max_tokens` small enough that the two numbers
can't coincide.

## The auth cache and the revocation window (2026-07-31)

The proxy caches **successful** API-key lookups in memory
([proxy/authcache.go](proxy/authcache.go)). This is the one place where a deliberate,
bounded weakening of key revocation was accepted in exchange for latency, so it is
recorded here rather than only at the code.

**What it changes.** `authorize()` no longer queries `api_keys` on every request. A key
hash that resolved successfully is reused for **up to 30 seconds** (`authCacheTTL`).

**Why.** Measured on the real binary against a production-shaped Supabase latency, that
lookup cost 91 ms of a 228 ms request; removing it took the request to 136 ms, a 40%
reduction (`docs/LATENCY_BASELINE.md` §B.1).

**The exposure, precisely.** Setting `active = false` on a key — the revocation path,
and the one *Deferred* below proposes to make self-service — does not take effect for up
to 30 seconds on any proxy instance that has served that key recently. With N replicas
each holding its own cache, the window is up to 30 s **per instance**, the same
per-instance shape as the rate limiter's documented residual.

**What bounds it.**

1. **Only successes are cached.** A revoked key that has *not* been seen recently is
   refused on the first try, and an invalid key is never cached at all — so this cannot
   be used to amplify credential stuffing, and there is no negative entry to poison.
2. **`POST /admin/auth-cache/flush`** drops every entry, making revocation immediate
   without a redeploy. Same auth shape as `/admin/metrics`: pre-auth admission,
   constant-time comparison over SHA-256 digests, and the route does not exist at all
   when `PROXY_ADMIN_TOKEN` is unset.
3. **The window is announced at startup**, and when no admin token is configured the
   startup line is a WARNing, because in that configuration the TTL is the only bound
   there is.
4. The cache is keyed by the SHA-256 the code already computes, never by the key, so it
   holds nothing the database does not already hold.

**What would make this unacceptable and require revisiting:** a compliance requirement
for immediate revocation; a move to per-user keys where revocation is a user-facing
security action rather than an operator one; or raising the TTL, which should not be
done without re-reading this section.

## Current state (2026-07-17)

- `auth.users`: **0 users.** The policies are dormant and will activate with the first real user.
- `api_keys`: 9 rows, **1 active** (`mochi_ad…`, "rotated-2026-07-14"). All others are
  deactivated test/disposable keys.
- All rows have `user_id IS NULL`, including `mochi_ad…` — it is invisible to every authenticated
  user, which is correct for now. It needs an owner once a real user exists.

## Deferred

- **Self-service key revocation** — a `SECURITY DEFINER` function flipping `active = false` on
  the caller's own rows only, never an UPDATE grant. Three requirements, all learned the hard way:
  **revoke EXECUTE from `PUBLIC` in the same migration** (trap 3 — this is now a hard rule, not a
  suggestion, and this function is the first place it bites for real rather than in depth); pin
  `set search_path = ''` with fully-qualified names, because a SECURITY DEFINER function with a
  mutable search_path is hijackable; and run the trap 3 probe afterward rather than trusting
  `pg_default_acl`.
- **PROPOSED, NOT APPLIED — `revoke usage on schema public from anon`.** A role without `USAGE` on
  the schema cannot reach anything inside it — table, view, or function — regardless of what
  default privileges hand out at creation. That would neutralize trap 3 entirely for the untrusted
  role, including the function half that `ALTER DEFAULT PRIVILEGES` can't fix. It does nothing for
  `authenticated`, so it is a partial control, not a replacement for the SECURITY DEFINER rule.
  **Unknowns, unprobed:** whether PostgREST needs `anon` to hold schema `USAGE` for schema-cache
  introspection or for its root/OpenAPI endpoint to respond sanely, and whether it degrades the
  denial from a clean 42501 into something less legible. `anon` already has zero grants on every
  object in `public`, so it *should* be a no-op that adds a blanket layer — but "should be" is not
  the standard used elsewhere in this document. Probe it against the anon key before applying.
- **Assign `mochi_ad…` an owner** once a real user exists.
- **Verify GoTrue's failed-delete transactionality** (trap 4).
- **Resolve the trap 3 function mechanism** — why `ALTER DEFAULT PRIVILEGES ... REVOKE EXECUTE ON
  FUNCTIONS FROM PUBLIC` removes the catalog entry without changing the behavior, contrary to
  PostgreSQL's documented example. Logged as unresolved rather than explained.

## Notes for whoever touches the database next

The project is on **PostgreSQL 17** (confirmed by the presence of the `MAINTAIN` privilege, added
in 17). `REVOKE ALL PRIVILEGES` covers `MAINTAIN` implicitly — `ALL PRIVILEGES` resolves against
the server's actual privilege set rather than an itemized list.

Catalog reads that answer the questions this document raises:

```sql
-- is RLS actually on?
select c.relname, c.relrowsecurity, c.relforcerowsecurity
from pg_class c join pg_namespace n on n.oid = c.relnamespace
where n.nspname = 'public' and c.relname in ('api_keys','usage');

-- what policies exist?
select schemaname, tablename, policyname, permissive, roles, cmd, qual, with_check
from pg_policies where schemaname = 'public' and tablename in ('api_keys','usage');

-- every grantee, unfiltered (information_schema hides grants the current role can't see)
select c.relname,
       case when a.grantee = 0 then 'PUBLIC' else pg_get_userbyid(a.grantee) end as grantee,
       a.privilege_type
from pg_class c join pg_namespace n on n.oid = c.relnamespace
cross join lateral aclexplode(c.relacl) a
where n.nspname = 'public' and c.relname in ('api_keys','usage','api_keys_public');

-- function grants, including the implicit EXECUTE-to-PUBLIC default
select p.proname, p.prosecdef,
       case when a.grantee = 0 then 'PUBLIC' else pg_get_userbyid(a.grantee) end as grantee,
       a.privilege_type
from pg_proc p join pg_namespace n on n.oid = p.pronamespace
cross join lateral aclexplode(coalesce(p.proacl, acldefault('f', p.proowner))) a
where n.nspname = 'public' and p.proname in ('reserve_usage','increment_usage');

-- FK delete behavior ('r' = RESTRICT, 'a' = NO ACTION, 'n' = SET NULL, 'c' = CASCADE)
select conname, confdeltype from pg_constraint
where conrelid = 'public.api_keys'::regclass and contype = 'f';
```

These require the Supabase SQL editor — the repo has no DB connection string, and PostgREST
exposes only `public`, so `pg_catalog` is unreachable from any tooling checked into this
repository. Same constraint as `proxy/migrations/0001_reserve_usage.sql`.

---

# The inference hop — ZDR retention finding

**Status: OPEN. Corroborated. Escalate to OpenRouter. Established 2026-07-17.**

Everything above is the database. This is the other half: what happens to a user's prompt once it
leaves the proxy.

## Enforcement and the guarantee are different things

Two claims. One is verified and settled. The other is in doubt. **This section exists partly to
stop them being blurred**, because "ZDR is verified" is true of the first and false of the second.

**The flags are enforced. This is not what's in question.** Every inference request carries
`{"zdr":true,"data_collection":"deny"}`; `daemon/config.go` resolves an absent or legacy config to
the *strictest* setting rather than the weakest; `proxy/main.go` forwards the request body
byte-for-byte, so the daemon's provider-routing object reaches OpenRouter intact; and OpenRouter
genuinely hard-refuses with a 404 (`No endpoints found matching your data policy (Zero data
retention)`) when no ZDR-compliant endpoint exists. Proven live on both a positive and a negative
path, and re-proven after `allow_fallbacks` was flipped to `true` — see BACKLOG's ZDR gate entry.

**The guarantee those flags are meant to deliver is in question.** A provider on OpenRouter's
ZDR-eligible list, serving a request that carried both flags, retained our prompt content between
requests.

Our code asks for the right thing and refuses correctly when it cannot get it. Whether the label it
asks against means anything is a separate question, and no change to our code can answer it.

## The finding

> Under `{"zdr":true,"data_collection":"deny"}`, DeepInfra served **11,776 of 11,970 tokens (98%)**
> of provably novel, CSPRNG-generated, never-before-sent content from cache, seconds after the
> byte-identical body was sent.

Established by re-running experiment E1 against a fresh novel prefix, through the deployed proxy,
provider pinned. Four sends — one cold, three warm, ~2s apart.

| send | `cached_tokens` | `native_tokens_cached` | prompt cost | `cache_discount` |
|---|---|---|---|---|
| #0 cold | 64 | 64 | 0.001072692 | 4.608e-06 |
| #1 warm | 64 (**miss**) | 64 | 0.001072692 | 4.608e-06 |
| #2 warm | **11,776** | **11,776** | **0.000229428** | **0.000847872** |
| #3 warm | **11,776** | **11,776** | **0.000229428** | **0.000847872** |

`prompt_tokens` = 11,970 and `cache_write_tokens` = 0 on all four. All four served by DeepInfra,
`endpoint_id` `934a69f9-bd54-474b-beca-24560f721e12`. Request body sha256
`a49145af971f3a7d2e49b80911932796745a63f6d62afb996ad96fdfa38910f8`, hash-verified identical on
every send.

## Three corroborations — and which one actually carries it

**Cite the billing. Do not cite the endpoint agreement.** A reader reaching for the strongest
evidence here will reach for the wrong one if this is not spelled out.

### 1. Billing — this is the one that carries it

Prompt cost fell **78.6%** on a byte-identical body: `0.001072692` → `0.000229428`, with an explicit
`cache_discount` of `0.000847872`. That is `11,776 × 7.2e-08` **exactly**, and it was **predicted to
six significant figures before the run**, from a rate derived from an earlier 64-token sample. The
pricing reconciles end to end: gross `11,970 × 9.0e-08 = 0.0010773`, minus the discount, equals the
charged prompt cost.

Why this and not the rest: **a provider does not forgo four-fifths of the revenue on a request it
actually computed.** Every other signal in this section is DeepInfra describing its own behavior,
and a self-report can be wrong in any direction. Billing can only be wrong in the direction that
costs them money. The money moved.

### 2. `/api/v1/generation` agreement — WEAK. Do not lean on it.

`native_tokens_cached` matched the inline `cached_tokens` at magnitude on every send
(64/64/11776/11776). This is near-certainly **the same upstream number surfaced through two
endpoints**, not an independent measurement. It rules out OpenRouter mangling the field in transit.
It does not test DeepInfra's honesty, which is the thing that matters. This caveat was stated
*before* the run; the result did not change it.

### 3. Reproduction

The 11,776-token hit landed on a fresh CSPRNG prefix distinct from the original E1's, then again on
the next send. Not a one-off.

## H4 is dead, and why it mattered

**H4** was the hypothesis that `cached_tokens` was an OpenRouter-side artifact — a mangled,
misattributed, or stale field reflecting nothing DeepInfra actually did. **It was the hypothesis
that would have collapsed the finding**, turning "a ZDR provider retains prompts" into "an
OpenRouter field is buggy." It was live because the original E1 rested on a single inline field with
nothing behind it.

It is dead: the independent endpoint agrees at magnitude, and — decisively — the billing moved by
exactly the amount the field predicts. A mangled field does not produce a correct discount.

Note which evidence killed it: **the billing, not the endpoint agreement.** Per the caveat above,
agreement between the two endpoints was always compatible with H4 in a different shape — one wrong
number surfaced twice.

## Scope limit — read before citing this

**Established:** content derived from a user's prompt persists between requests on a ZDR-labeled
route, long enough to be matched and reused seconds later, and the provider bills accordingly.

**Not established. Do not imply any of it:**

- **What is retained.** KV tensors, raw token text, a content hash plus a handle to precomputed
  state — unknown, and not measurable from outside.
- **Where it lives.** GPU memory, host RAM, disk, a shared multi-tenant store — unknown.
- **How long it survives.** **TTL was never measured.** See below.
- **Whether anyone else can reach it.** No cross-account or cross-key test was ever run. Every send
  in every experiment used our own account and our own key.
- **Whether this constitutes "retention" under OpenRouter's ZDR contract.** A policy question about
  what the label promises, not a technical one. It cannot be settled from this side, and no reading
  of it should be asserted here. **It is the question to put to OpenRouter.**

One inference, flagged as inference rather than measurement: a hit requires the system to have
retained enough state to *recognize* our prefix and to *skip recomputing* it. The second half
implies retained computation derived from user content, not merely a fingerprint of it. That follows
from how prefix caching is generally understood to work. It was not observed.

### What measuring TTL would take

Not done. The blocker is not cost — the inference is ~$0.03.

Design: a cold send, then a warm repeat at each of a series of delays (30s, 2m, 5m, 15m, 1h, …),
provider pinned, looking for the delay at which the hit stops landing.

**The confound that makes the naive version worthless:** misses are stochastic. Send #1 above missed
at +2s with the cache demonstrably warm — #2 and #3 hit moments later on the same `endpoint_id`.
**A single miss at delay T proves nothing about expiry at T.** Separating "expired" from "missed
anyway" needs enough replicates per delay point to distinguish the two rates, and **the stochastic
miss rate has never been characterized**: the only samples behind it are 2 misses in 5 warm sends in
one experiment, and 1 miss in 3 warm sends in this one. That is not a rate.

Compounding it: a hit plausibly refreshes the entry, so replicates cannot share a prefix — each
(delay, replicate) needs its own CSPRNG-novel prefix. Feasible, but not a small experiment, and it
should not start until the miss rate is characterized first.

## Two loose observations from the generation records

Recorded because they were seen, not because they are understood. Neither is a finding, and neither
is guessed at.

- **`"data_region": "global"`** appears in the `/api/v1/generation` record for a request that
  carried `zdr:true, data_collection:deny`. Its semantics were not established and no meaning is
  asserted here. **Worth putting to OpenRouter alongside the cache question.**
  `response_cache_source_id` was present and `null` on all four sends; also unexplained.
- **`endpoint_id` was identical across every send — the hits and the miss alike.** Whatever drives
  hit/miss variance sits *below* the endpoint OpenRouter routes to. Data, not an explanation.

Separately, OpenRouter's own `latency` field corroborates the independently measured conclusion that
caching buys no latency here: 958ms cold, 3879ms on the miss, **1068ms on the 11,776-token hit**,
1654ms on the second hit — the hit was *slower* than the cold send. `provider_responses[].latency`
was 260/254/259/262ms, flat across cold and cached.

## Still open — do not cite either as settled

- **The 256 plateau.** A plateau at 256 cached tokens, observed during the cache-mechanism
  investigation, was never explained. No mechanism, no account of the conditions that produce it.
  Not resolved by this run, and deliberately not re-litigated by it.
- **The per-worker-cache hypothesis.** The leading explanation for stochastic misses — that the
  cache is per-worker and routing within an endpoint is non-deterministic — is **the leading
  explanation and nothing more. It has never been tested as a mechanism.** Worker identity is not
  exposed in any field OpenRouter returns, which is exactly why it was never tested. Do not present
  it as the reason for the misses.

## Reproduction

The point of this section: re-run it without rebuilding the reasoning.

**Design constraints. Each is load-bearing:**

1. **CSPRNG-novel prefix.** Content that provably has never been sent to any provider by anyone.
   Generate it fresh per run — reusing this document's prefix proves nothing. Construction used:
   `random.Random(secrets.randbits(64))`, 3,400 × 6-char lowercase words → 23,799 chars → 11,970
   `prompt_tokens`. Record its sha256.
2. **Pin the provider, kill fallbacks.**
   `{"zdr":true,"data_collection":"deny","allow_fallbacks":false,"order":["DeepInfra"]}`. Without
   the pin, a miss is indistinguishable from having been routed elsewhere. **Keep both ZDR flags
   set** — the entire claim is that this happens *under* ZDR. Confirm the serving provider from the
   response rather than assuming the pin held.
3. **Byte-identical body, hash-verified.** sha256 the serialized body; assert it is identical across
   every send. A prefix-cache claim on a body you did not verify is worthless.
4. **Through the deployed proxy.** No bypass needed and none is acceptable: `proxy/main.go` forwards
   the body byte-for-byte, so the pin survives the hop. Bypassing would change what the test proves.
   `user_agent: "Go-http-client/2.0"` in the generation record confirms the proxy path was used.
5. **Cold + warm repeats**, ~2s apart, n ≥ 4. **Expect misses.** One warm miss does not falsify the
   finding — send #1 missed. It is the stochastic behavior above.
6. **Fixed trivial user turn** (`"Say OK and nothing else."`) so completion cost stays negligible
   and only the prompt side varies.
7. **Disposable key, deactivated in a `finally` block, targeted by `id` — never `key_prefix`.** See
   BACKLOG's Hygiene note: `key_prefix` is not unique and this has bitten twice.

**The three checks:**

| check | question | 2026-07-17 result |
|---|---|---|
| 1 | Does `/api/v1/generation`'s `native_tokens_cached` agree with the inline `cached_tokens` **at magnitude**? | Agrees (11,776 = 11,776). **Weak** — same number, two endpoints. |
| 2 | **Does the money move?** Does `cache_discount` / prompt cost drop by the amount the hit implies? | Yes — 78.6% drop; discount `0.000847872` = 11,776 × 7.2e-08, predicted in advance. **This is the check that decides it.** |
| 3 | Does it reproduce on a fresh novel prefix? | Yes, twice — with one miss (send #1). |

**Predict the numbers before running.** Check 2 is only strong evidence if the discount is derived
from a pricing model *first* and then confirmed. A discount observed and rationalized afterward
proves far less.

Full run cost: 4 inference sends + 4 generation lookups, ~$0.0026.

---

# The daemon socket — trust model, and the Gate 7 / FAIL-3 closure

Added 2026-07-30 (Phase 4). The two sections above cover the database and the
inference hop. This one covers the third surface: the local Unix socket the CLI,
TUI and IDE integration use to reach the daemon. It exists because FAIL-3's own
record named the trust model as *the* open question — "everything else in FAIL-3
is secondary until this is decided" — and that question was answered by
implementation without the answer ever being written down as a model.

## The model in one paragraph

The daemon listens on a Unix socket at `protocol.SocketPath()`, under a per-user
runtime directory, chmod 0600. **File permissions are not the access control** —
they narrow who can reach the socket, but they do not identify who did. The
access control is `authorizePeer` (`daemon/server_auth.go`): the kernel reports
the connecting process's UID via `SO_PEERCRED`, and a peer whose UID differs from
the daemon's own is refused before the handshake is read, before any request is
decoded, and before anything is dispatched. Every legitimate client runs as the
user who started the daemon, so same-UID is exactly the trusted set — verified
with zero client-side configuration and nothing for a user to get wrong.

**The trust boundary is therefore the OS user account, not the process.** A
same-UID process is trusted completely. This is a deliberate choice, and the
sections below say what it costs.

## Why the credential cannot be forged, and why it fails closed

`SO_PEERCRED` is filled in by the kernel at `connect()` time from the peer's real
credentials. It is not read from the wire, so nothing a client sends can change
it — this is the property that makes it access control rather than a convention.

Every failure path refuses:

| Condition | Result |
|---|---|
| Peer UID ≠ daemon UID | refused |
| Connection exposes no peer credentials (`syscall.Conn` type assertion fails) | refused |
| `getsockopt` itself fails | refused |
| Non-Linux build (`peercred_other.go`) | refused — there is no `SO_PEERCRED` equivalent wired up, and the daemon declines rather than degrading to trust-everyone |
| `os.Getuid()` reports −1 (no UID concept) | refused — the daemon never trusts a peer it cannot meaningfully compare against |

The refusal reason goes to the daemon's own log and is never returned to the
rejected peer, so peer auth adds no information-leakage surface of its own.

`checkPeerUID` is split out as a pure function precisely so the match/mismatch
decision is unit-testable: an unprivileged test process cannot construct a real
cross-UID socket, and a security decision that can only be exercised by a setup
the test suite cannot build is a security decision that never gets tested.

## Gate 7 — what was fixed

Error responses returned over the socket used to carry absolute filesystem paths
and internal path-resolution detail. Fixed in `3aeb8b6`: `scrubPaths` and
`socketSafeError` (`daemon/server_errors.go`) replace workspace roots with a
token, and upstream model-API errors are returned generically while the real
error is logged locally.

Enforced by assertion, not by commit message — four tests in
`daemon/gate7_scrub_test.go`:

- `TestHandleApplyEdit_ScrubsAbsolutePathsFromErrorResponses`
- `TestHandleUndo_ScrubsAbsoluteBackupsPathFromErrorResponses`
- `TestServeConn_ModelAPIErrorIsGenericAndLogsUpstreamLocally`
- `TestServeConn_ZDRRefusalMessageUnchanged`

The last one matters more than its name suggests: scrubbing must not flatten the
ZDR refusal into a generic error, because that refusal is a *guarantee* the user
is entitled to see. Scrub-everything would have been the easy fix and the wrong
one.

## Accepted residual — the existence oracle

**Not fixed, accepted by decision.** Error responses still distinguish "this file
does not exist" from "this file exists but is refused" (a secret name, a
protected directory, outside the workspace root). An attacker who could reach the
socket could use it to probe for the presence of individual paths.

Accepted because of who that attacker can be. Post-`SO_PEERCRED`, the only peer
that reaches a response at all is an authenticated same-UID process — which can
already call `stat()` on any path it likes and read `/proc` directly. The oracle
tells such a peer nothing it cannot obtain more cheaply by other means, so
closing it would buy no confidentiality against the only audience that exists.

The cost of closing it is real: unifying the socket's error vocabulary means
collapsing distinctions across every handler's error paths, and the same
flattening that hides "refused vs absent" from an attacker also hides it from the
user, whose "why did my edit not apply?" is answered by exactly that distinction.
Gate 7's own fix was careful to scrub paths while *preserving* diagnostic
meaning; unification would spend that.

**This acceptance is conditional. Reopen it if any of the following becomes
true:**

1. The socket becomes reachable by a UID other than the daemon's — a shared
   service account, a container with multiple identities, a `sudo`-invoked client.
2. The transport stops being a local Unix socket — any TCP or network listener,
   including localhost-only.
3. Any client-facing surface begins relaying daemon error text onward, so that a
   response can travel off-box.

Each of these changes *who is listening*, which is the entire basis on which the
residual is accepted. None of them changes the code.

## Logged, not closed

These stay open with their severity stated. They are not part of the acceptance
above and are not covered by it.

- **Mid-ancestor-directory-swap TOCTOU in `confinedRestorePath`.** The undo path
  resolves the deepest existing ancestor, checks containment, then relies on
  `O_NOFOLLOW` for the leaf. A symlink swapped into a *mid-ancestor* directory
  inside that window is not blocked. Local-write-access-gated, so the attacker is
  already inside the trust boundary — but the socket gives them unlimited free
  retries at winning the race, which raises its practical urgency without changing
  its impact ceiling.
- **`id_rsa_secret.pub`-style narrow over-refusal in `MatchesSecretName`.** The
  substring net fires before the `.pub` carve-out is reached, so a public-key name
  that also contains "secret" or "credential" is refused. This fails in the safe
  direction — over-refusing a public key, never under-refusing a secret — which is
  why it is logged rather than fixed.
- **SQLite `-wal`/`-shm` symlink opens.** The driver owns those opens, so the
  leaf-lstat guard on the db file does not cover them. Distinct from their file
  *modes*, which Phase 4 fixed (BACKLOG item (g)).

## The embedder helper socket — a second listener, a different rule

Added 2026-08-09, while preparing D1 for a ruling. The section above describes
*the* socket as though there were one. There are two, and until now only one was
written down.

The daemon spawns an embedder helper (`helper/main.go`) and reaches it over its
own Unix socket at `helperproto.SocketPath(daemonPID)` — inside the same 0700
`protocol.SocketDir()`, chmod 0600, named per daemon PID. That helper has its own
accept loop, and **it does not call `authorizePeer`**. No peer credential is read;
no UID is compared.

So the two listeners enforce "same-uid" by two different mechanisms:

| | Daemon socket | Helper socket |
|---|---|---|
| Parent directory | 0700 `SocketDir()` | 0700 `SocketDir()` (same) |
| Socket mode | 0600 | 0600 |
| Peer credential | **`authorizePeer`** — kernel-supplied UID/SID, fails closed | **none** |
| What actually admits a peer | the kernel's answer to *who connected* | the filesystem's answer to *who may open this path* |

This is worth stating plainly because the paragraph above is emphatic in the
other direction: *"File permissions are not the access control — they narrow who
can reach the socket, but they do not identify who did."* For the helper socket,
file permissions **are** the access control. That is the honest description.

**One consequence does not carry over.** L7 (the umask window between `net.Listen`
and `chmod 0600`) is recorded as "narrowed by the 0700 runtime directory and
closed in practice by `SO_PEERCRED`" — see the note at
[`protocol/transport_unix.go`](protocol/transport_unix.go). The second clause is
true only of the daemon socket. On the helper socket that window is closed by the
0700 directory **alone**, because there is no peer check behind it.

**Why this is recorded rather than fixed.** The helper's job is to turn text into
vectors. It holds no credential, no workspace handle and no write path; its
dispatch surface is one embed request. An attacker who could reach it — meaning
one who is already inside a 0700 directory owned by this user, i.e. already
same-uid — gains the ability to compute embeddings. Adding peer authentication
there is cheap and defensible, but it is a change to a security boundary and
therefore belongs to a ruling, not to a tidy-up.

**This is a scope question for D1**, and it is the reason it is written here:
"is same-uid-implies-trusted the socket's auth model?" currently has two answers,
and the ruling should say whether it covers both listeners or only the daemon's.
Either answer is defensible. What was not defensible was leaving the asymmetry
undocumented, so that D1 would be ruled on a description of the system that was
accurate about one socket and silent about the other.

**Enforced, not just described.** `daemon/socketauthcoverage_test.go` classifies
every accept loop in the product and fails the build on an unclassified one. The
helper is listed there as an explicit, reasoned exemption rather than an
omission, and the test refuses an exemption with no written justification. If a
third listener ever appears, it fails until somebody classifies it — which is the
moment to reopen D1 and D3, and previously depended on somebody remembering to.

## Status

Gate 7 and the FAIL-3 socket axis are **engineering-complete and closed by written
rationale as of 2026-07-30**, with the residual above accepted on the stated
conditions. As everywhere else in this program, **nothing here is founder-closed**
— the final call on FAIL-3 remains the founder's, and this document exists to make
that call reviewable rather than to pre-empt it.

---

# Agent mode — two trust lanes, and what we will not claim

Added 2026-08-01 (Phase 5–7). The three sections above cover the database, the
inference hop, and the local socket. This one covers the fourth surface, and the
first one that can make this product *do* things rather than say them: the
agentic tool loop and the MCP servers it can reach.

It exists because the honest description of this feature is uncomfortable in one
specific place, and a security document that omits the uncomfortable part is
marketing.

## The model in one paragraph

Agent mode is **off by default** (`mcp.enabled`). With it on, the daemon may run
a bounded loop: model call → tool call → model call, until the model stops asking
or one of four per-turn ceilings bites. Tools come from two places that share
nothing but a type. **Lane A** is Go functions compiled into the daemon —
confined by the same path/secret resolver that gates every model-proposed edit,
and *none of them writes*: the edit tool proposes into the existing five-gate
review. **Lane B** is any MCP server the user configured, spawned as a stdio
subprocess. Every tool resolves to `deny`, `ask` or `allow` from the user's own
`models.json`; anything unlisted resolves to `ask`, and `ask` suspends the turn
until a human answers on the same socket connection the turn is streaming over.

## The claim, stated exactly

**For Lane B we claim consent and audit. We do not claim containment, and we
will not.**

An MCP server is an ordinary process running with the user's full privileges.
The five gates constrain *our* writer; they have no reach into somebody else's
subprocess. There is no OS sandbox here — no seccomp, no namespace, no
`landlock`. If a configured server decides to read `~/.ssh` and post it
somewhere, nothing in this product stops it.

What is true, and what every approval prompt says in these words:

- it does not start unless the user configured it (and acknowledged, per server,
  in writing, that it is unconfined);
- it does not run unless the user approves that specific call;
- the user sees the **complete** arguments first, never a summary;
- every decision is written to a local append-only log.

That is a real protection and a narrower one than "sandboxed". The distinction
is load-bearing: a user who believes Lane B is contained will configure servers
they would otherwise refuse.

## What the approval actually binds

The prompt carries the exact argument bytes **and their SHA-256**. The client
echoes the digest back unchanged; the daemon re-checks it, plus the call id,
before dispatching. What was shown and what runs are provably the same object.

This is the same class of check `VerifyUnchanged` makes for edits, for the same
reason: between rendering a thing for a human and acting on it, something must
prove the thing did not change.

Four independent checks reject an answer, and **every one of them denies rather
than errors**:

| Check | Failure means |
|---|---|
| Exact-key sniff on `approval` | the body is not recognisably an answer (`{"APPROVAL":true}` is not one — same discipline as the request dispatcher) |
| `call_id` echo | it is an answer to some other question |
| `arguments_sha256` echo | the arguments shown are not the arguments about to run |
| Decision in the defined set, with `approval:true` behind an approving verb | an invented verb is not a permission |

A timeout is a denial. A closed connection is a denial. No approval channel at
all is a denial. **Silence is never consent**, and the code has no path on which
"we could not ask" resolves to anything but "no".

## Credentials cannot reach a Lane B server

A spawned server's environment is built from scratch, not inherited: it gets
`PATH` and `HOME`, plus any variable the user explicitly allow-listed. Three
names — `OPENROUTER_API_KEY`, `CODETERMINAL_API_KEY`, `CODETERMINAL_MOCHIII_KEY`
— are **ungrantable**: a config that asks for one is refused outright rather than
spawning and filtering.

Neutering that construction to `os.Environ()` leaks the inference key, the
managed-mode key, `SSH_AUTH_SOCK`, and ~130 other variables. That is the measured
consequence, and it is why the environment is built rather than pruned.

## Tool output is egress, and is treated as egress

Whatever a tool returns goes to the model, which means it leaves the machine. It
passes through the same secret scrubber and the same truncation as retrieved
chunks, at a single choke point, **truncating before scrubbing** — scrubbing first
changes the length and could slice a redaction placeholder in half.

Two of the four per-turn ceilings exist for this specifically: per-result bytes
and cumulative tool bytes per turn. A privacy-positioned product should be able
to bound and report how much extra left the machine because of tools, and the
activity stream reports the post-scrub figure per call.

## The audit log

`.codeterminal/logs/toolcalls.jsonl` — local file only, append-only, `O_NOFOLLOW`,
size-rotated, `0600`. There is deliberately no `io.Writer` seam and no network
path; it shares its substrate with the warn-mode sink for exactly that reason.

**One record per dispatch decision, including calls that were never prompted**
(policy `allow`). An audit covering only what the user already watched happen is
a log of things they already knew.

**It records the argument digest and length, never the arguments.** Those are
unscrubbed model output — paths, queries, source text — and are the one field
that would make this file worth stealing. The digest is enough to bind a record
to the call that ran; it is the same digest the approval was bound to.

A write failure is swallowed, so a full disk means a call runs unrecorded rather
than a turn failing. That is a real trade and the same one the warn-mode sink
makes.

## Accepted residuals

- **Lane B is unconfined.** Stated above; accepted deliberately, mitigated by
  default-off, per-server acknowledgement, per-call consent, and audit. OS
  sandboxing is a non-goal for v1, not an oversight.
- **A tool's self-description is never a gate.** `readOnlyHint` and `destructive`
  are the server's own claims, carried for display only. Letting a
  self-description lower the bar would make consent optional for any server
  willing to lie, which is the entire population that matters.
- **Shutdown mid-approval has a narrow race.** Cancelling pushes the read
  deadline into the past to unblock the wait, but `limitedConn.Read` re-arms it
  immediately before each read, so a cancellation landing in a few-instruction
  window is overwritten and the wait runs its full length. Losing that race costs
  a slow shutdown, never a wrong decision — an unanswered ask is a denial either
  way.
- **Reliability is measured, not proven.** 30 real agent turns, zero
  non-termination and zero repeated calls
  (`docs/AGENT_LOOP_RELIABILITY_2026-07-31.md`). The honest claim is "no failures
  observed in 30 turns", not "never fails"; the 95% interval runs to roughly
  [88%, 100%].

## Status

Implemented and gate-green as of 2026-08-01, with the residuals above accepted on
the stated conditions. As everywhere else in this program, **nothing here is
founder-closed** — this document exists to make that call reviewable rather than
to pre-empt it.
