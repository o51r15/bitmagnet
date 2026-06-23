-- +goose Up
-- +goose StatementBegin

-- Fix for Issue #496: queue_jobs fetch performance
--
-- PROBLEM 1: The ORDER BY in handleJob uses a computed boolean expression
-- (status = 'retry') DESC which cannot be satisfied by a standard B-tree
-- index. Postgres falls back to a full table scan + in-memory sort of ALL
-- pending/retry rows on every single queue poll. At 60k+ pending jobs this
-- costs ~447ms and ~54MB RAM per query. Multiplied across all worker
-- concurrency, this is the primary source of DB memory pressure.
--
-- FIX: An expression index that pre-computes the boolean, dropping query
-- time from ~447ms to ~0.23ms (~2000x improvement per benchmark in #496).

CREATE INDEX queue_jobs_fetch_order_idx
    ON queue_jobs (queue, (status = 'retry') DESC, priority, run_after, id)
    WHERE status IN ('pending', 'retry');

-- PROBLEM 2: Migration 00012 created a GIN index on (queue, payload).
-- Per pg_stat_user_indexes this index has accumulated 678MB in size and
-- has 0 index scans - it is never used for reads. Every INSERT, UPDATE,
-- and DELETE to queue_jobs pays GIN index maintenance cost for no benefit.
--
-- FIX: Drop it. The fingerprint and status indexes cover all real queries.

DROP INDEX IF EXISTS queue_jobs_queue_payload_idx;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS queue_jobs_fetch_order_idx;
CREATE INDEX ON queue_jobs USING gin(queue, payload);

-- +goose StatementEnd
