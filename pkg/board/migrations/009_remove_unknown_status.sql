-- Reconcile activity through the existing event stream after startup.
UPDATE cards SET status = 'waiting' WHERE status = 'unknown';
