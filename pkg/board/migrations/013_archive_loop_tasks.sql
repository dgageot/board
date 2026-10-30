-- Preserve task history and comparison points without restarting removed loops.
ALTER TABLE cards ADD COLUMN diff_base TEXT NOT NULL DEFAULT '';
UPDATE cards SET status = 'closed',
    diff_base = (SELECT base FROM loop_project_tasks WHERE card_id = cards.id)
WHERE id IN (SELECT card_id FROM loop_project_tasks);
