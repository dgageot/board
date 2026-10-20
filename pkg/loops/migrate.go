package loops

import (
	"context"
	"database/sql"
	"fmt"
)

// Additive migrations preserve existing loops and their run histories.
func migrateProjects(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, change := range []struct{ table, column, definition string }{
		{"loops", "project_id", "TEXT NOT NULL DEFAULT ''"},
		{"loop_jobs", "project", "TEXT NOT NULL DEFAULT '{}'"},
	} {
		found, err := hasColumn(ctx, tx, change.table, change.column)
		if err != nil {
			return err
		}
		if !found {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", change.table, change.column, change.definition)); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func hasColumn(ctx context.Context, tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var value any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &value, &pk); err != nil {
			return false, err
		}
		found = found || name == column
	}
	return found, rows.Err()
}
