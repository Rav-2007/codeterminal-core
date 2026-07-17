# Security model — auth, RLS, and grants

Covers the Supabase posture for `api_keys`, `usage`, the `api_keys_public` view, and the two
usage RPCs. Everything here was verified live against the production project
(`eshpqodurxubjigndiev`) on **2026-07-17** using an anon key plus a real user JWT — not with
`service_role`, which holds BYPASSRLS and passes every test regardless of whether RLS works.
That distinction is the whole point of this document: **a `service_role` check proves nothing.**

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

## Notes for whoever touches this next

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
