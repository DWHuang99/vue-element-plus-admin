-- 000013_evidence_cleanup_audit.down.sql

DROP FUNCTION public.finalize_cleanup_audit(UUID, TEXT, JSONB, TEXT);
DROP FUNCTION public.record_cleanup_audit(UUID, TEXT, TEXT, TEXT, TEXT, TEXT, TIMESTAMPTZ, TIMESTAMPTZ, JSONB);
DROP TABLE platform_evidence_cleanup_audit;
