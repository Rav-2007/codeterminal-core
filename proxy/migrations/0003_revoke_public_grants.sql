-- STATUS: APPLIED to live Supabase production 2026-07-27, verified before/after.
--   Table grants (gap #1): anon/authenticated/PUBLIC held ZERO privileges on
--     pending_corrections both before and after -- that gap was never open.
--   Function EXECUTE (gaps #2, #3): PUBLIC could EXECUTE all three before
--     (reserve_usage / apply_correction / sweep_pending_corrections = true);
--     after the two revokes below, all three = false. Real, now closed.
--   Containment while open: all three functions are SECURITY INVOKER (no
--     SECURITY clause in 0002 -> Postgres default), so an anon caller ran as
--     anon and the inner UPDATE usage still failed on usage's SELECT-only
--     grants -- a defense-in-depth regression, not a live write hole.
--
-- Corrective for the grant/EXECUTE surface migration 0002 left open. The
-- mid-incident call that 0002's new pending_corrections table "needed no RLS
-- because the proxy is service_role and bypasses RLS anyway" is a non-sequitur:
-- RLS and grants constrain the anon / authenticated roles (the PostgREST /
-- frontend-reachable path), NOT service_role. service_role holding BYPASSRLS
-- says nothing about whether anon/authenticated can reach the table. The call
-- never checked the load-bearing thing, which is the GRANT surface.
--
-- Three concrete gaps this closes:
--
--   1. pending_corrections was created (0002) with ZERO grant/revoke statements.
--      Supabase's bootstrap has historically auto-granted full anon CRUD on new
--      relations in public (the 2026-07-17 posture's "trap 3": the catalog reads
--      clean while the hole is open). If that fired here, SELECT leaks every
--      key's key_id+reserved+created_at (cross-tenant metadata, Low), and
--      DELETE/INSERT are an INTEGRITY concern -- DELETE wipes outbox rows and
--      defeats the crash-recovery sweep that is the entire purpose of 0002;
--      INSERT floods phantom stranded-reservation rows the sweep reports as real.
--
--   2. 0002's `drop function if exists reserve_usage(uuid,integer)` + recreate
--      silently dropped the 2026-07-17 REVOKE EXECUTE FROM PUBLIC on it
--      (dropping a function drops its ACL; the recreated function reverts to the
--      EXECUTE-to-PUBLIC baseline). Defense-in-depth regression, not a live hole
--      -- reserve_usage is SECURITY INVOKER, so an anon caller runs as anon and
--      the inner UPDATE usage still fails on usage's SELECT-only grants -- but it
--      undoes an established control while the catalog reads clean.
--
--   3. apply_correction / sweep_pending_corrections are new (0002) SECURITY
--      INVOKER functions that also carry the default EXECUTE-to-PUBLIC.
--
-- The grant/EXECUTE revokes below are the load-bearing fix, mirroring the
-- 2026-07-17 discipline that every such function revoke EXECUTE-from-PUBLIC in
-- the same migration that (re)creates it.
--
-- Applied via the Supabase SQL editor -- this repo has no DB connection string
-- or psql/supabase CLI wired up (QUOTA_RESERVATION_DESIGN.md §7). This file is
-- NOT auto-run from here; it is the tracked source of record for the dashboard-
-- applied change. See the STATUS banner at the top of this file for the
-- verified before/after results.
--
-- VERIFY FIRST (run before this migration; apply only if it shows a gap):
--
--   -- Does anon/authenticated/PUBLIC hold ANY privilege on the new table?
--   select grantee, privilege_type
--   from information_schema.role_table_grants
--   where table_schema = 'public'
--     and table_name   = 'pending_corrections'
--     and grantee in ('anon', 'authenticated', 'PUBLIC');
--
--   -- Can PUBLIC EXECUTE the three functions 0002 (re)created?
--   select 'reserve_usage' as fn,
--          has_function_privilege('public','reserve_usage(uuid,integer)','EXECUTE') as public_can_execute
--   union all select 'apply_correction',
--          has_function_privilege('public','apply_correction(uuid,integer,bigint)','EXECUTE')
--   union all select 'sweep_pending_corrections',
--          has_function_privilege('public','sweep_pending_corrections(integer)','EXECUTE');
--
-- RE-VERIFY AFTER: re-run both queries. Expected: zero rows from the first;
-- public_can_execute = false on all three from the second.

revoke all on pending_corrections from anon, authenticated;

revoke execute on function
  reserve_usage(uuid, integer),
  apply_correction(uuid, integer, bigint),
  sweep_pending_corrections(integer)
from public;

-- Optional belt-and-suspenders: SELECT-only RLS parity with usage/api_keys. The
-- revokes above are the actual fix; this is not required. No policy is created
-- for anon/authenticated, so with RLS on and grants revoked they have no path
-- in, while service_role (BYPASSRLS) is unaffected. Uncomment to enable.
--
-- alter table pending_corrections enable row level security;
