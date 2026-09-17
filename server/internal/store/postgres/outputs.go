package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/cadence/server/internal/domain"
	"github.com/cadence/server/internal/store"
	"github.com/jackc/pgx/v5"
)

// Outputs (0015): what a work item produced. Create / list / delete only —
// an output is never edited, so there is no patch path and no version.

const outputCols = `id::text, tenant_id::text, task_id::text, kind, title, url, detail, created_by::text, created_at`

func scanOutput(row pgx.Row) (domain.Output, error) {
	var o domain.Output
	err := row.Scan(&o.ID, &o.TenantID, &o.TaskID, &o.Kind, &o.Title, &o.URL, &o.Detail, &o.CreatedBy, &o.CreatedAt)
	return o, err
}

func (s *Store) CreateOutput(ctx context.Context, tenantID, actorID, taskID string, in domain.CreateOutputInput) (*domain.Output, error) {
	o, err := store.NewOutput(tenantID, actorID, taskID, in, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	err = s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := getTaskTx(ctx, tx, tenantID, taskID, false); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO outputs (tenant_id, id, task_id, kind, title, url, detail, created_by, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			o.TenantID, o.ID, o.TaskID, o.Kind, o.Title, o.URL, o.Detail, o.CreatedBy, o.CreatedAt); err != nil {
			return err
		}
		return emitEntity(ctx, tx, tenantID, actorID, domain.EventOutputCreated, domain.EntityOutput, o.ID, o)
	})
	if err != nil {
		return nil, err
	}
	return &o, nil
}

func (s *Store) ListOutputs(ctx context.Context, tenantID, taskID string) ([]domain.Output, error) {
	out := []domain.Output{}
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := getTaskTx(ctx, tx, tenantID, taskID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+outputCols+` FROM outputs WHERE tenant_id = $1 AND task_id = $2 ORDER BY created_at, id`, tenantID, taskID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			o, err := scanOutput(rows)
			if err != nil {
				return err
			}
			out = append(out, o)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) DeleteOutput(ctx context.Context, tenantID, actorID, taskID, id string) error {
	return s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `DELETE FROM outputs WHERE id = $1 AND tenant_id = $2 AND task_id = $3`, id, tenantID, taskID)
		if err != nil {
			return notFoundOnBadUUID(err) // non-uuid id: not found
		}
		if ct.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return emitEntity(ctx, tx, tenantID, actorID, domain.EventOutputDeleted, domain.EntityOutput, id, nil)
	})
}

// unmarshalInto decodes a jsonb column into dest, treating an empty column as
// the zero value (jsonb NOT NULL DEFAULT '{}' never yields empty, but a
// scanned nil is harmless).
func unmarshalInto(b []byte, dest any) error {
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, dest)
}
