# Migration runbook — `0000` and `0004`

**Audience:** whoever holds Supabase dashboard access.
**Time:** ~5 minutes. Four read-only queries, one `revoke`, one re-check.
**Where:** the Supabase SQL editor. This repo has no DB connection string and no
`psql`/`supabase` CLI wired up ([`QUOTA_RESERVATION_DESIGN.md`](../QUOTA_RESERVATION_DESIGN.md) §7),
so nothing here is auto-run and nothing in `proxy/migrations/` executes from CI.

This exists because the two migration headers each carry their own verify block,
one of them is now half-answered, and asking someone to reconcile that across two
files while logged into production is how steps get skipped. Everything still
outstanding is collected here, in order, with the expected answer stated *before*
you run it.

---

## Already answered — do not redo

Reconciled 2026-07-30 by read-only PostgREST probes (`GET /rest/v1/` and
`GET /rest/v1/usage?select=key_id` as `service_role`). Recorded in
[`proxy/migrations/0000_baseline_usage_api_keys.sql`](../proxy/migrations/0000_baseline_usage_api_keys.sql).

| Question | Answer |
|---|---|
| **Is `usage.key_id` UNIQUE?** | **Yes — it is the PRIMARY KEY.** `reserve_usage`'s cross join cannot multiply. |
| Is there a FK from `usage.key_id`? | Yes, to `api_keys.id`. Previously "FK GUESSED". |
| Duplicate `key_id` rows right now? | No. 15 rows, zero duplicates. |
| Columns / types / nullability / defaults | Verified for both tables; `0000` now matches production. |

`0000` was corrected as a result: `usage.token_limit` has a default of `100000`
(the file declared none), `usage.period_start` was **missing entirely**, and
`api_keys` was missing `key_prefix` (NOT NULL, no default), `label`, and
`user_id`.

**A relation nobody in this repo knew about turned up:** `api_keys_public`,
exposed over PostgREST, in no migration. It projects
`(id, key_prefix, label, active, created_at, user_id)` — `api_keys` **minus**
`key_hash`, which reads as a deliberately safe public projection. Query 3 below
now covers it. That is metadata, not key material, but it is cross-tenant
metadata on an unaudited relation.

---

## Part 1 — Read-only. Run all of this first, as one block.

Nothing here writes. Paste the whole block; four result grids come back.

```sql
-- 1. Every constraint on `usage`. The uniqueness question is already settled
--    (key_id is the PK); this catches anything else -- checks, extra FKs, an
--    index someone added from the dashboard.
--    EXPECT: a PRIMARY KEY on (key_id) and a FOREIGN KEY to api_keys(id).
--    Read the FK's ON DELETE action -- 0000 claims `on delete cascade` as intent
--    and marks it unverified. If it differs, correct 0000.
select c.conname, c.contype, pg_get_constraintdef(c.oid) as definition
from pg_constraint c
where c.conrelid = 'usage'::regclass;

-- 2. Every constraint on `api_keys`. THE ONE THAT MATTERS: is there a UNIQUE on
--    key_hash? PostgREST could only confirm the primary key on `id`.
--    EXPECT: PRIMARY KEY (id), plus UNIQUE (key_hash).
--    IF UNIQUE (key_hash) IS ABSENT: two rows could share a hash, and
--    authorize's "exactly one row" check (proxy/main.go:795) then fails CLOSED
--    -- that key is locked out entirely, no way in. Not a security hole; an
--    availability landmine. Add the constraint.
select c.conname, c.contype, pg_get_constraintdef(c.oid) as definition
from pg_constraint c
where c.conrelid = 'api_keys'::regclass;

-- 3. Grant surface, mirroring 0003's discipline -- extended to api_keys_public.
--    EXPECT: zero rows. The proxy reaches these tables only as service_role,
--    which bypasses both grants and RLS, so anon/authenticated need nothing.
--    ANY ROW IS A FINDING. Specifically:
--      usage / api_keys      -> SELECT leaks quota and key metadata;
--                               INSERT/UPDATE/DELETE is a billing integrity hole.
--      api_keys_public       -> SELECT makes every key prefix, label and user_id
--                               in the system readable by any unauthenticated
--                               caller. key_hash is excluded from the view, so
--                               no credential leaks -- but this relation is in no
--                               migration and has never been audited.
select table_name, grantee, privilege_type
from information_schema.role_table_grants
where table_schema = 'public'
  and table_name in ('usage', 'api_keys', 'api_keys_public')
  and grantee in ('anon', 'authenticated', 'PUBLIC')
order by table_name, grantee;

-- 4. 0004's gap, and the canonical version of this query -- 0003's three-row
--    variant is SUPERSEDED and must not be used to certify this surface again.
--    EXPECT: increment_usage = true (the gap 0004 closes), and the other three
--    = false (0003's work, applied and verified 2026-07-27).
--    If increment_usage is ALREADY false, 0004 is a no-op -- skip Part 2 and
--    just update its banner to say so.
select 'increment_usage' as fn,
       has_function_privilege('public','increment_usage(uuid,integer)','EXECUTE') as public_can_execute
union all select 'reserve_usage',
       has_function_privilege('public','reserve_usage(uuid,integer)','EXECUTE')
union all select 'apply_correction',
       has_function_privilege('public','apply_correction(uuid,integer,bigint)','EXECUTE')
union all select 'sweep_pending_corrections',
       has_function_privilege('public','sweep_pending_corrections(integer)','EXECUTE');

-- 5. The containment check 0004's entire severity assessment rests on.
--    prosecdef = false means SECURITY INVOKER: an anon caller executes the
--    function AS anon, so the inner `update usage` still fails on usage's own
--    grants. That is what makes query 4's `true` a defense-in-depth gap rather
--    than a live write hole.
--    EXPECT: false on all four rows.
--    IF ANY ROW IS true (SECURITY DEFINER): STOP. 0004's stated severity is
--    WRONG, that function is a live hole, and increment_usage in particular is
--    an unconditional signed `tokens_used = tokens_used + p_tokens` with no
--    token_limit check -- reachable means any key's usage can be set to
--    anything, including negative. Revoke first, then re-assess.
select p.proname, p.prosecdef as is_security_definer
from pg_proc p
join pg_namespace n on n.oid = p.pronamespace
where n.nspname = 'public'
  and p.proname in ('increment_usage','reserve_usage','apply_correction',
                    'sweep_pending_corrections');
```

---

## Part 2 — Apply `0004`. One statement.

Only if query 4 showed `increment_usage = true`.

```sql
revoke execute on function increment_usage(uuid, integer) from public;
```

This is the entire content of
[`proxy/migrations/0004_revoke_increment_usage.sql`](../proxy/migrations/0004_revoke_increment_usage.sql).
It removes a grant; it does not drop the function, change its body, or touch a
row. The proxy no longer calls `increment_usage` at all — `apply_correction`
(0002) replaced it at the only call site, `correctUsage` — so nothing in this
repo can be affected either way.

Dropping the function instead was considered and rejected: nothing outside this
repo's view is *known* not to call it (a dashboard job, a scheduled task, a
manual runbook), and dropping a live function is a riskier change than revoking a
grant on it. That stays a separate, deliberate decision.

---

## Part 3 — Re-verify, then write down what happened.

Re-run **query 4** only.

**Expect: `public_can_execute = false` on all four rows.**

Then update the STATUS banner at the top of
`proxy/migrations/0004_revoke_increment_usage.sql`, in the format `0003`
established — before/after result plus a date. It currently reads
`STATUS: NOT APPLIED.` Replace it with something of this shape:

```
-- STATUS: APPLIED to live Supabase production <DATE>, verified before/after.
--   Before: increment_usage EXECUTE-to-PUBLIC = true (the gap); the other three
--     = false (0003, 2026-07-27).
--   After:  all four = false.
--   Containment confirmed: all four functions prosecdef = false (SECURITY
--     INVOKER), so the severity stated in this file holds.
```

If any Part 1 query returned something unexpected — a missing
`unique (key_hash)`, a non-empty grant result, a `SECURITY DEFINER` function,
an `ON DELETE` action that isn't `cascade` — record it and stop rather than
adjusting the file to match. Those are findings, not paperwork.

---

## Order, and why it matters

1. **Part 1 before Part 2.** Query 5 is what tells you whether Part 2 is a
   routine hardening step or an incident.
2. **`0000` is never applied.** It is `create table if not exists` against tables
   that already exist — inert by construction. It is a *record*, and the standing
   instruction in its own banner is that the file gets corrected to match
   production, never the reverse.
3. **Migrations before deploys, always.** Not relevant to `0004` (it changes no
   behaviour the proxy depends on), but it is the rule `0002` had to learn: the
   Go side calls `apply_correction` on every correction, and against a database
   where the migration had not landed that RPC 404s — a 4xx, which `correctUsage`
   treats as non-retryable, so every correction on every request would be lost
   outright.
