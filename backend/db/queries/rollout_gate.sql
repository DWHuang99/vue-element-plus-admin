-- name: RecordRolloutGateSample :one
SELECT record_rollout_gate_sample(
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14
) AS gate_id;

-- name: ApproveRouteDisable :one
SELECT approve_route_disable(
    $1, $2, $3, $4
) AS gate_id;
