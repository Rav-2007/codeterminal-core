-- Closes QUOTA_RESERVATION_DESIGN.md §5(e): a process that crashes between a
-- successful reservation and its correction leaves tokens_used stranded at the
-- reserved value with NOTHING anywhere that knows a correction is owed. §5(d)'s
-- bounded retry cannot help -- there is no process left to retry with -- and
-- §5's direction analysis showed the stranding skews UNSAFE (understated usage,
-- so a later request is admitted against quota that was really spent) whenever
-- the crashed request's actual usage would have exceeded its reservation.
--
-- The fix is a durable outbox: every reservation opens a pending_corrections
-- row in the SAME atomic statement that reserves, every correction closes it in
-- the same atomic statement that increments, and a periodic sweep claims
-- anything left behind. This does NOT recover the crashed request's true usage
-- -- that number died with the process that would have reported it. It converts
-- a silent, indefinite, unsafe-direction stranding into a loud, time-bounded one
-- resolved in the SAFE direction (kept as charged, never refunded on missing
-- data), matching the asymmetry finalizeUsage already applies elsewhere.
--
-- Apply via the Supabase SQL editor -- this repo still has no DB connection
-- string or psql/supabase CLI wired up (QUOTA_RESERVATION_DESIGN.md §7).
--
-- DEPLOY ORDER MATTERS: apply this migration BEFORE shipping the proxy build
-- that calls apply_correction. The Go side calls apply_correction for every
-- correction; against a database where this migration has not landed, that RPC
-- 404s, and a 404 is a 4xx, which correctUsage treats as non-retryable -- so
-- every correction on every request would be lost outright.

-- The outbox. One row per live reservation: opened by reserve_usage, closed by
-- apply_correction, and claimed by sweep_pending_corrections only if neither
-- ever happened (i.e. the proxy died holding it).
--
-- reserved is carried on the row so a swept entry can be reported with the
-- amount that is now permanently charged, which is the whole operational point
-- of the log line it produces. No usage figure is stored because none exists at
-- reservation time -- the correction is what would have carried it.
create table if not exists pending_corrections (
  id         bigint      generated always as identity primary key,
  key_id     uuid        not null,
  reserved   integer     not null,
  created_at timestamptz not null default now()
);

-- The sweep's only predicate is on created_at, and the table is expected to hold
-- roughly (in-flight request count) rows in steady state -- tiny. The index is
-- here so the sweep stays a bounded index scan rather than a seq scan if the
-- table ever grows through a sustained incident.
create index if not exists pending_corrections_created_at_idx
  on pending_corrections (created_at);

-- reserve_usage gains a third returned column (pending_id), so it is DROPped and
-- recreated rather than CREATE OR REPLACEd: Postgres refuses to replace a
-- function whose OUT-parameter/return-table shape changed. Signature (uuid,
-- integer) is unchanged, so callers other than the return shape are unaffected.
--
-- Behavior of the reservation itself is IDENTICAL to 0001: the same atomic
-- UPDATE ... WHERE ... RETURNING, so the TOCTOU closure 0001 established is
-- preserved exactly. What is new is that opening the outbox row happens inside
-- the same statement, via a data-modifying CTE. That matters for the crash case
-- this migration exists for: a separate INSERT would reintroduce, in the outbox,
-- precisely the crash-in-the-gap hole the outbox is supposed to close.
--
-- Zero rows back still means "refused" (no usage row, or over token_limit). When
-- the reserved CTE yields no rows, the INSERT selects from it and so inserts
-- nothing, and the final join yields nothing -- a refusal opens no outbox row.
drop function if exists reserve_usage(uuid, integer);
create function reserve_usage(p_key_id uuid, p_reserved integer)
returns table(tokens_used bigint, token_limit bigint, pending_id bigint)
language sql
as $$
  with reserved as (
    update usage
    set tokens_used = usage.tokens_used + p_reserved
    where usage.key_id = p_key_id
      and usage.tokens_used + p_reserved <= usage.token_limit
    returning usage.tokens_used, usage.token_limit
  ), opened as (
    insert into pending_corrections (key_id, reserved)
    select p_key_id, p_reserved from reserved
    returning pending_corrections.id
  )
  select reserved.tokens_used, reserved.token_limit, opened.id
  from reserved, opened;
$$;

-- Replaces the direct increment_usage call in proxy/main.go's correctUsage.
-- Same increment increment_usage always did (p_tokens is a signed delta: negative
-- for a refund, positive for a top-up, zero for "keep the reservation as the
-- charge"), plus closing the outbox row -- in ONE statement, so the two cannot
-- diverge. If they could, a crash between them would either double-charge on the
-- next sweep or drop a correction, which is the class of bug being closed here.
--
-- increment_usage is deliberately left in place, unchanged. It is no longer
-- called by the proxy, but dropping a live function is a separate, riskier
-- change than adding one, and nothing here requires it to be gone.
--
-- p_pending_id may reference a row that no longer exists (a sweep already
-- claimed it -- possible if a correction arrives after the stale window, e.g.
-- a very long correctUsage retry against a degraded database). The DELETE
-- simply matches nothing in that case. The usage increment still applies, which
-- is correct: the sweep never refunded anything, so applying a late top-up is
-- still the right adjustment, and a late refund can only move tokens_used back
-- toward truth.
create or replace function apply_correction(p_key_id uuid, p_tokens integer, p_pending_id bigint)
returns void
language sql
as $$
  with bumped as (
    update usage
    set tokens_used = usage.tokens_used + p_tokens
    where usage.key_id = p_key_id
    returning 1
  )
  delete from pending_corrections
  where pending_corrections.id = p_pending_id;
$$;

-- Claims every reservation older than p_stale_minutes and hands it back to the
-- caller to be logged. DELETE ... RETURNING is a single atomic statement, so a
-- row is claimed by exactly one caller even with several proxy replicas (or an
-- overlapping slow sweep) running this concurrently: the losers' DELETEs
-- re-evaluate against the committed state and match nothing, so no swept row is
-- ever reported twice.
--
-- Deliberately NO refund. The swept reservation stays charged in tokens_used.
-- The true usage of a request whose process died is not recoverable from
-- anywhere, and of the two available guesses, refunding is the unsafe one: it
-- would hand back quota for inference OpenRouter really did bill. Keeping the
-- charge over-meters at worst, in the direction that can only throttle early.
--
-- p_stale_minutes must exceed the longest legitimate request lifetime or live
-- requests would be swept out from under themselves. The proxy passes 15
-- minutes against a 5-minute upstreamTimeout and a 6-minute serverWriteTimeout
-- (proxy/main.go) -- a >2x margin over the longest request that can exist.
create or replace function sweep_pending_corrections(p_stale_minutes integer)
returns table(id bigint, key_id uuid, reserved integer, created_at timestamptz)
language sql
as $$
  delete from pending_corrections
  where pending_corrections.created_at < now() - make_interval(mins => p_stale_minutes)
  returning pending_corrections.id, pending_corrections.key_id,
            pending_corrections.reserved, pending_corrections.created_at;
$$;
