# Domain Event Contract

**Feature**: `004-microservice-splitting`  
**Delivery in this feature**: PostgreSQL transactional outbox → in-process dispatcher → Organization inbox  
**Broker**: none

## Principles

- delivery is **at least once**；consumers must be idempotent。
- event creation is atomic with producer domain change。
- producer marks published only after consumer success。
- event IDs are globally unique and stable across retries。
- event name and schema version are explicit；version incompatibility is not silently ignored。
- payload is minimal and excludes passwords, tokens, hashes and unnecessary PII。
- event records are not public HTTP models。

## Envelope v1

```json
{
  "event_id": "1d2b1228-6c17-4f21-b4ce-38d88e5c6f11",
  "event_type": "iam.user.deleted",
  "event_version": 1,
  "producer": "iam",
  "aggregate_type": "user",
  "aggregate_id": "42",
  "aggregate_version": 7,
  "occurred_at": "2026-08-10T12:00:00Z",
  "correlation_id": "request-or-workflow-id",
  "payload": {
    "user_id": 42
  }
}
```

### Required fields

| Field | Rule |
|---|---|
| `event_id` | UUID generated once at producer transaction time；never regenerated on retry。 |
| `event_type` | Stable lowercase dotted name without version suffix；version is separate field。 |
| `event_version` | Positive integer schema version。 |
| `producer` | Stable owner identifier, not hostname/pod name。 |
| `aggregate_type` | Stable aggregate category。 |
| `aggregate_id` | String representation for transport neutrality。 |
| `aggregate_version` | Monotonic aggregate version/tombstone version used for ordering safeguards。 |
| `occurred_at` | UTC timestamp from domain change, not dispatch time。 |
| `correlation_id` | Request/workflow correlation；must not be raw session token。 |
| `payload` | Versioned JSON object containing only consumer-required data。 |

## Event catalog

### `iam.user.deleted` version 1

**Producer**: IAM  
**Consumer**: Organization handler `remove-membership-on-user-deleted`  
**When emitted**: each user deleted by an IAM authoritative delete transaction  
**Purpose**: remove Organization membership without cross-domain transaction/FK

Payload:

```json
{"user_id": 42}
```

**Producer transaction guarantee**:

- user deletion, IAM session/role cleanup, delete command receipt and outbox insert commit together；
- `aggregate_version` is the deleted user's previous `users.version + 1` tombstone version；
- if transaction rolls back, neither deletion nor event exists；
- a retried idempotent delete operation does not create additional logical deletion events。

**Consumer guarantee**:

- validate producer/type/version/aggregate/payload identity, then delete membership state and insert successful inbox dedupe in one Organization transaction；
- required tuple: producer=`iam`, type=`iam.user.deleted`, version=`1`, aggregate_type=`user`, positive aggregate_version, canonical aggregate_id equal to string(payload.user_id)；
- missing membership state is success；
- duplicate `event_id` for same handler is success without repeated side effect；
- unsupported/mismatched contract is not acknowledged, writes no inbox row, and becomes producer-blocked/observable。

## Outbox delivery state

```text
pending --claim--> leased --ack--> published
   ^                 |
   |                 +--record-failure(transient)--> pending
   +--lease expiry---+
                     +--block(non-retryable)----> blocked
```

Durable fields include `status`, `available_at`, `delivery_epoch`, `epoch_started_at` (equal to the epoch's initial `available_at`), current-epoch `attempt_count`, cross-epoch `total_attempt_count`, `lease_owner`, `leased_until`, `claim_token`, `last_error_code`, `blocked_at`, `blocked_reason_code`, and `published_at` with status-consistency checks. Immutable `iam_outbox_requeues` records each resulting epoch, trusted recovery identity/authorization/approval/correlation, approved reason, and the previous epoch start/deadline, block time/reason, last error and attempt counters.

Dispatcher behavior through IAM `OutboxDeliveryPort`:

1. in a short IAM transaction select bounded eligible pending/expired-lease rows with `FOR UPDATE SKIP LOCKED`。Rows already past `epoch_started_at + 24h` or with 20 attempts are atomically blocked without a new lease；otherwise persist `leased` + owner + expiry + fresh claim token and increment current-epoch/total attempt counters；
2. commit/release producer transaction before decode/consumer work；each fresh claim/reclaim is an attempt, so worker crash after claim still consumes budget；
3. decode/validate envelope and invoke Organization through its event port；
4. on success `Ack(event_id, claim_token)` CAS to published；
5. on transient failure `RecordOutboxFailure(...claim_token...)` atomically evaluates the already-counted attempt/time ceiling and performs exactly one transition: pending with safe error/bounded backoff, or blocked `delivery_exhausted`；
6. on unsupported/non-retryable schema/contract failure `Block(...claim_token...)` durably and alert without hot-loop；
7. `RequeueBlocked(event_id, trusted_recovery_context, approved_reason, available_at)` is available only through an authenticated Platform operations boundary；it increments delivery epoch, sets `epoch_started_at=available_at`, resets only epoch attempts, preserves total attempts, and atomically appends immutable complete prior epoch/block evidence；
8. stale worker ack/failure/block with an old claim token is rejected；expired lease is recoverable and duplicate delivery remains expected；
9. blocked events cannot be waived/purged in this feature；they remain blocked or are authorized/requeued and eventually published；
10. graceful shutdown stops new claims and lets current calls finish until deadline; unfinished leases expire safely。

Integration MUST NOT import IAM sqlc or mutate producer tables directly. A producer database transaction is never held across the consumer call.

## Inbox idempotency

Identity key:

```text
(event_id, handler_name)
```

Rules:

- same event may have multiple independent handlers in future；each has separate inbox identity。
- row presence means that handler side effect committed successfully；duplicate returns success。
- invalid/unsupported/failing attempts do not write this successful row；producer outbox retains retry/blocked ownership and observability。
- business side effect and successful dedupe marker commit in same transaction, so concurrent duplicate work has one effective commit。
- successful inbox dedupe is retained at least 30 days and never shorter than any retained producer event capable of replay；blocked producer events prevent related dedupe cleanup。

## Ordering

- global ordering is not guaranteed。
- per aggregate, `aggregate_version` is monotonic and consumers must not allow an older state-changing event to overwrite newer state。
- deletion v1 is terminal for the referenced IAM user ID because IDs are not reused。
- dispatcher may process different aggregates concurrently；contract correctness cannot depend on batch order。

## Versioning rules

### Compatible additions

A producer may add optional payload fields under the same version only when old consumers ignore unknown fields and existing field semantics/types do not change.

### New version required

Increment `event_version` when:

- required field added/removed；
- type or meaning changes；
- privacy classification changes；
- consumer action changes incompatibly。

During version transition, producer/consumer compatibility and rollback window must be documented. Consumer must branch explicitly by version.

### Event type change

Use a new event type when the business fact itself changes, not merely its encoding.

## Privacy and security

Event envelope/payload MUST NOT include:

- plaintext password or password hash；
- raw session/access/refresh token or token hash；
- database URL/credential；
- permission list when consumer does not need it；
- username/email/account for `iam.user.deleted` version 1；
- HTTP Authorization header；
- stack trace or raw SQL error。

Logs may include event ID, type/version, aggregate ID, attempt number, correlation ID and safe error code. Payload logging defaults to disabled.

## Failure and reconciliation

Required observability:

- pending count；
- oldest pending age；
- dispatch attempt count and latency；
- published count；
- blocked/non-retryable count；
- inbox processed/duplicate and handler-failure count；
- leased count/oldest lease, lease-recovery count, stale-claim rejection；
- compensation/reconciliation alerts by operation ID。

Reconciliation command/process may compare deleted IAM IDs/events with Organization memberships. It must operate through owner ports or reviewed operational SQL and must not expose credentials.

Retention: pending/leased/blocked events are never purged automatically；blocked has no waiver-to-purge path in this feature. Published events remain at least 30 days. Immutable requeue history remains at least 30 days after both final publish and last requeue, never shorter than the associated event/workflow/rollback evidence, and is not cascade-deleted while unresolved. Inbox dedupe remains at least as long. Immediate rollback retains all unresolved and claim/attempt/requeue evidence.

## Testing

Required failure-injection cases:

1. IAM deletion rolls back before commit → no event, user remains。
2. IAM deletion commits, dispatcher not run → event durable pending。
3. consumer fails before Organization commit → membership remains, no successful inbox row, atomic failure recording returns event to pending/backoff or blocks an exhausted epoch。
4. consumer commits, producer acknowledgement fails/worker crashes → lease expiry + duplicate delivery, no duplicate side effect。
5. same event delivered concurrently → one effective handler transaction。
6. unsupported version or producer/aggregate/payload identity mismatch → no membership/inbox mutation, event enters durable blocked state and alerts。
7. repeated transient failure reaches one epoch's attempt/time ceiling through the atomic failure transition → `blocked(delivery_exhausted)`; no further automatic hot retry；an epoch past its deadline is blocked during claim scan without a new consumer call。
8. controlled requeue requires trusted authenticated recovery context, sets epoch start to delayed eligibility, preserves total attempts, and writes immutable full previous-epoch evidence；unauthorized/unapproved or non-blocked requeue is rejected。
9. worker crashes after claim → the attempt remains counted；after lease expiry another worker reclaims and increments again；stale original claim cannot ack/record failure。
10. dispatcher restarts with backlog → all retryable events eventually processed under healthy dependencies。
11. event/log scan → no password/token/hash/username/email leakage。

## Deferred decisions

The following are intentionally deferred to the physical Organization extraction feature:

- broker selection；
- gRPC versus broker boundary for other interactions；
- cross-network authentication/mTLS；
- dead-letter infrastructure outside PostgreSQL；
- multi-region ordering and retention；
- additional Organization-produced events。
