-- 000012_rollout_gate_functions.down.sql
-- Drop the Platform rollout-gate functions; the append-only evidence table
-- (compatibility_rollout_gates, migration 000008) is retained as evidence.
DROP FUNCTION public.approve_route_disable(TEXT, TEXT, TEXT, TIMESTAMPTZ);
DROP FUNCTION public.record_rollout_gate_sample(TEXT, TEXT, BIGINT, BOOLEAN, BIGINT, BIGINT, TEXT, BIGINT, TIMESTAMPTZ, TEXT, TEXT, TEXT, TIMESTAMPTZ, INTERVAL);
