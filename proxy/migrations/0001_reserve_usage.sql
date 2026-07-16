-- Closes the quota-check TOCTOU race documented in
-- QUOTA_RESERVATION_DESIGN.md: checkQuota (a SELECT) and recordUsage (a
-- write applied only after the response completed) were separate round
-- trips with an unguarded gap, so concurrent requests could all read the
-- same stale "under limit" state and all get admitted. Confirmed live via
-- an 8-concurrent test: 8/8 requests admitted against a 100-token limit,
-- final usage landed at 236.
--
-- Schema referenced below (usage.key_id uuid, usage.tokens_used bigint,
-- usage.token_limit bigint not null, api_keys.id uuid) was read from the
-- live project's PostgREST OpenAPI document, not assumed.
--
-- Apply via the Supabase SQL editor -- this repo has no DB connection
-- string or psql/supabase CLI wired up to apply it automatically.

-- increment_usage already exists live today and is NOT being changed --
-- reconstructed here only so it's captured in version control for the
-- first time (it previously existed only in the Supabase dashboard).
-- Behavior matches proxy/main.go's existing documented contract at its
-- call site: "tokens_used = tokens_used + tokens", no other side effects.
-- Reused unchanged by the new reservation flow for the post-response
-- true-up step (delta can be positive or negative -- a correction is just
-- a signed amount to add).
create or replace function increment_usage(p_key_id uuid, p_tokens integer)
returns void
language sql
as $$
  update usage
  set tokens_used = usage.tokens_used + p_tokens
  where usage.key_id = p_key_id;
$$;

-- New. Atomically reserves p_reserved tokens against p_key_id's quota
-- before the request is forwarded to the model, closing the race: the
-- check ("would this stay under token_limit?") and the write (actually
-- reserving it) are now the same statement, so a concurrent second call
-- against the same key_id blocks on that row's lock until the first
-- commits, then re-evaluates its own WHERE clause against the
-- now-committed tokens_used -- it can never observe the pre-reservation
-- value the first call already claimed against.
--
-- Returns zero rows if the key has no usage row, or if reserving
-- p_reserved would exceed token_limit -- callers (proxy/main.go's
-- reserveQuota) treat an empty result as "refuse, 429 before the model is
-- ever contacted," fail-closed, same posture as every other gate in this
-- proxy.
create or replace function reserve_usage(p_key_id uuid, p_reserved integer)
returns table(tokens_used bigint, token_limit bigint)
language sql
as $$
  update usage
  set tokens_used = usage.tokens_used + p_reserved
  where usage.key_id = p_key_id
    and usage.tokens_used + p_reserved <= usage.token_limit
  returning usage.tokens_used, usage.token_limit;
$$;
