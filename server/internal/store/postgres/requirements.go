package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cadence/server/internal/domain"
	"github.com/cadence/server/internal/store"
	"github.com/jackc/pgx/v5"
)

const requirementCols = `id::text, tenant_id::text, project_id::text, title, description, weight, acceptance,
	status, position, archived_at, version, created_at, updated_at`

func scanRequirement(row pgx.Row) (domain.Requirement, error) {
	var r domain.Requirement
	err := row.Scan(&r.ID, &r.TenantID, &r.ProjectID, &r.Title, &r.Description, &r.Weight, &r.Acceptance,
		&r.Status, &r.Position, &r.ArchivedAt, &r.Version, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

// selectRequirements always fences by tenant_id ($1) as its own predicate —
// isolation must not depend solely on RLS (see selectTasks).
func selectRequirements(ctx context.Context, tx pgx.Tx, tenantID string, f store.RequirementFilter) ([]domain.Requirement, error) {
	q := `SELECT ` + requirementCols + ` FROM requirements WHERE tenant_id = $1`
	args := []any{tenantID}
	if f.ProjectID != nil {
		args = append(args, *f.ProjectID)
		q += fmt.Sprintf(` AND project_id = $%d`, len(args))
	}
	q += ` ORDER BY position, created_at`
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Requirement
	for rows.Next() {
		r, err := scanRequirement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) ListRequirements(ctx context.Context, tenantID string, f store.RequirementFilter) ([]domain.Requirement, error) {
	var out []domain.Requirement
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rs, err := selectRequirements(ctx, tx, tenantID, f)
		if err != nil {
			return emptyOnBadUUID(err) // a non-uuid projectId matches nothing
		}
		out = rs
		return nil
	})
	return out, err
}

func (s *Store) CreateRequirement(ctx context.Context, tenantID, actorID string, in domain.CreateRequirementInput) (*domain.Requirement, error) {
	title := trim(in.Title)
	if title == "" {
		return nil, domain.Invalid("title", "is required")
	}
	if len(title) > store.MaxTitleLen {
		return nil, domain.Invalid("title", "is too long")
	}
	if strings.TrimSpace(in.ProjectID) == "" {
		return nil, domain.Invalid("projectId", "is required")
	}
	if err := store.CheckTextLen("description", deref(in.Description)); err != nil {
		return nil, err
	}
	if err := store.CheckTextLen("acceptance", deref(in.Acceptance)); err != nil {
		return nil, err
	}
	weight := domain.RequirementWeightDefault
	if in.Weight != nil {
		if *in.Weight < domain.RequirementWeightMin || *in.Weight > domain.RequirementWeightMax {
			return nil, domain.Invalid("weight", "must be a whole number from 1 to 5")
		}
		weight = *in.Weight
	}
	status := orDefault(in.Status, domain.RequirementOpen)
	if !domain.ValidRequirementStatus(status) {
		return nil, domain.Invalid("status", "must be one of open, met, dropped")
	}
	now := time.Now().UTC().Truncate(time.Microsecond) // see CreateProject
	r := domain.Requirement{
		ID: domain.NewID(), TenantID: tenantID, ProjectID: in.ProjectID, Title: title,
		Description: deref(in.Description), Weight: weight, Acceptance: deref(in.Acceptance),
		Status: status, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if in.Position != nil {
		r.Position = *in.Position
	}
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := requireProject(ctx, tx, tenantID, &r.ProjectID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO requirements (tenant_id, id, project_id, title, description, weight, acceptance, status, position, version, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			r.TenantID, r.ID, r.ProjectID, r.Title, r.Description, r.Weight, r.Acceptance, r.Status, r.Position, r.Version, r.CreatedAt, r.UpdatedAt); err != nil {
			return err
		}
		return emitEntity(ctx, tx, tenantID, actorID, domain.EventRequirementCreated, domain.EntityRequirement, r.ID, r)
	})
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) UpdateRequirement(ctx context.Context, tenantID, actorID, id string, patch map[string]any, expectedVersion *int) (*domain.Requirement, error) {
	now := time.Now().UTC()
	var updated *domain.Requirement
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		r, err := scanRequirement(tx.QueryRow(ctx,
			`SELECT `+requirementCols+` FROM requirements WHERE id = $1 AND tenant_id = $2 FOR UPDATE`, id, tenantID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return notFoundOnBadUUID(err)
		}
		if expectedVersion != nil && *expectedVersion != r.Version {
			return domain.ErrConflict
		}
		if err := store.ApplyRequirementPatch(&r, patch, now); err != nil {
			return err
		}
		if err := requireProject(ctx, tx, tenantID, &r.ProjectID); err != nil {
			return err
		}
		r.Version++
		// RETURNING updated_at so the response/event carry the trigger-bumped
		// timestamp (matches the memory adapter, which sets UpdatedAt to now).
		if err := tx.QueryRow(ctx, `
			UPDATE requirements SET project_id=$2, title=$3, description=$4, weight=$5, acceptance=$6,
				status=$7, position=$8, archived_at=$9, version=$10
			WHERE id=$1 AND tenant_id=$11 RETURNING updated_at`,
			id, r.ProjectID, r.Title, r.Description, r.Weight, r.Acceptance, r.Status, r.Position, r.ArchivedAt, r.Version, tenantID).
			Scan(&r.UpdatedAt); err != nil {
			return err
		}
		updated = &r
		return emitEntity(ctx, tx, tenantID, actorID, domain.EventRequirementUpdated, domain.EntityRequirement, id, r)
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *Store) DeleteRequirement(ctx context.Context, tenantID, actorID, id string) error {
	return s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `DELETE FROM requirements WHERE id = $1 AND tenant_id = $2`, id, tenantID)
		if err != nil {
			return notFoundOnBadUUID(err)
		}
		if ct.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return emitEntity(ctx, tx, tenantID, actorID, domain.EventRequirementDeleted, domain.EntityRequirement, id, nil)
	})
}
