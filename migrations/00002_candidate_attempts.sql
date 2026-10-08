-- +goose Up
ALTER TABLE problem_publications ADD COLUMN attempted_problem_ids uuid[] NOT NULL DEFAULT '{}';
-- Previously reserved candidates remain unavailable for another preparation
-- attempt in this publication. Counts from old versions remain authoritative.
UPDATE problem_publications SET attempted_problem_ids=ARRAY[problem_id] WHERE problem_id IS NOT NULL;

-- +goose Down
ALTER TABLE problem_publications DROP COLUMN attempted_problem_ids;
