package cache

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/atyronesmith/ai-helps-jira/internal/fieldmeta"
)

// ReplaceFields atomically replaces the entire tenant-visible field catalog.
func (c *Cache) ReplaceFields(ctx context.Context, fields []fieldmeta.Field, refreshedAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if refreshedAt.IsZero() {
		return fmt.Errorf("field refresh timestamp is required")
	}
	tx, err := c.beginWrite()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM field_catalog"); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, "INSERT INTO field_catalog(id,field_json) VALUES(?,?)")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, f := range fields {
		if f.ID == "" {
			return fmt.Errorf("field metadata has empty ID")
		}
		b, e := json.Marshal(f)
		if e != nil {
			return e
		}
		if _, e = stmt.ExecContext(ctx, f.ID, string(b)); e != nil {
			return fmt.Errorf("store field %s: %w", f.ID, e)
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO field_catalog_state(id,refreshed_at) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET refreshed_at=excluded.refreshed_at", refreshedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

// GetFields returns a catalog snapshot and its last successful refresh time.
// A zero-row catalog with a timestamp is distinct from an uninitialized cache.
func (c *Cache) GetFields(ctx context.Context) ([]fieldmeta.Field, time.Time, error) {
	tx, err := c.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, time.Time{}, err
	}
	defer tx.Rollback()
	var stamp string
	err = tx.QueryRowContext(ctx, "SELECT refreshed_at FROM field_catalog_state WHERE id=1").Scan(&stamp)
	if err != nil && err != sql.ErrNoRows {
		return nil, time.Time{}, err
	}
	var refreshed time.Time
	if err == nil {
		refreshed, err = time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("invalid field refresh timestamp: %w", err)
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT field_json FROM field_catalog ORDER BY id")
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()
	fields := make([]fieldmeta.Field, 0)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, time.Time{}, err
		}
		var f fieldmeta.Field
		if err := json.Unmarshal([]byte(raw), &f); err != nil {
			return nil, time.Time{}, err
		}
		fields = append(fields, f)
	}
	if err := rows.Err(); err != nil {
		return nil, time.Time{}, err
	}
	if err := rows.Close(); err != nil {
		return nil, time.Time{}, err
	}
	if err := tx.Commit(); err != nil {
		return nil, time.Time{}, err
	}
	return fields, refreshed, nil
}
