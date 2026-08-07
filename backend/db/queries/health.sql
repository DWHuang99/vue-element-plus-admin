-- Health check query for sqlc.
-- Used to verify database connectivity during readiness checks.
-- Currently unused by scaffold (health check uses pgx Ping directly),
-- but serves as a sqlc query placeholder for future database-dependent checks.

-- name: HealthCheck :one
SELECT 1 AS healthy;
