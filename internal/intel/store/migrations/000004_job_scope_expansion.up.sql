-- Persisted scope expansion state for large intel jobs.
--
-- Existing jobs are complete because they already contain their full item set.
-- New paged-scope jobs set scope_mode='paged', scope_complete=0 and advance
-- scope_cursor together with each bounded job_items batch.
ALTER TABLE jobs ADD COLUMN scope_mode TEXT NOT NULL DEFAULT 'materialized';
ALTER TABLE jobs ADD COLUMN scope_complete INTEGER NOT NULL DEFAULT 1;
ALTER TABLE jobs ADD COLUMN scope_cursor TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN scope_scanned INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN scope_matched INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN scope_enqueued INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN scope_skipped INTEGER NOT NULL DEFAULT 0;
