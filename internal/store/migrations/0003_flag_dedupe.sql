-- Dedupe pre-existing duplicate flags before enforcing identity uniqueness.
-- Pre-fix inserts raced on (grade_id, type, passage, violated), so legacy
-- databases may already hold duplicates; creating the index first would abort
-- with "UNIQUE constraint failed". Delete first, then index -- the survivor
-- per identity prefers a triaged (non-open) row, then the most recent row
-- (created_at, then id), so triage work is never silently discarded.
DELETE FROM flags
WHERE id NOT IN (
  SELECT id FROM (
    SELECT id, ROW_NUMBER() OVER (
      PARTITION BY grade_id, type, passage, violated
      ORDER BY CASE WHEN status = 'open' THEN 1 ELSE 0 END ASC,
               created_at DESC, id DESC
    ) AS rn
    FROM flags
  )
  WHERE rn = 1
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_flags_grade_identity
ON flags(grade_id, type, passage, violated);
