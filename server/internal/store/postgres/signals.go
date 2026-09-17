package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cadence/server/internal/domain"
	"github.com/cadence/server/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The signal surface (0014): sources, signals + bodies + participants, and
// origins. Every statement carries its own tenant_id predicate on top of RLS
// (see selectTasks); every write emits through emitEntity with sig.Meta() —
// the row never leaves in an event, the body never leaves the body query.

// ---- sources ------------------------------------------------------------------

const sourceCols = `id::text, tenant_id::text, kind, name, owner_id::text, config, consent_at,
	retention_days, disabled_at, version, created_at, updated_at`

func scanSource(row pgx.Row) (domain.Source, error) {
	var s domain.Source
	err := row.Scan(&s.ID, &s.TenantID, &s.Kind, &s.Name, &s.OwnerID, &s.Config, &s.ConsentAt,
		&s.RetentionDays, &s.DisabledAt, &s.Version, &s.CreatedAt, &s.UpdatedAt)
	s.Normalize()
	return s, err
}

func (s *Store) GetOrCreateManualSource(ctx context.Context, tenantID string) (*domain.Source, error) {
	var out *domain.Source
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		src, err := manualSourceTx(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		out = &src
		return nil
	})
	return out, err
}

// manualSourceTx returns the tenant's manual source, inserting it on first
// use. The insert is ON CONFLICT DO NOTHING against sources_manual_uniq, so
// two concurrent first captures converge on one row; the loser re-selects.
func manualSourceTx(ctx context.Context, tx pgx.Tx, tenantID string) (domain.Source, error) {
	find := func() (domain.Source, bool, error) {
		src, err := scanSource(tx.QueryRow(ctx,
			`SELECT `+sourceCols+` FROM sources WHERE tenant_id = $1 AND kind = $2`, tenantID, domain.SourceManual))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Source{}, false, nil
		}
		return src, err == nil, err
	}
	if src, ok, err := find(); err != nil || ok {
		return src, err
	}
	src := store.NewManualSource(tenantID, time.Now().UTC())
	ct, err := tx.Exec(ctx, `
		INSERT INTO sources (tenant_id, id, kind, name, config, version, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7,$8) ON CONFLICT DO NOTHING`,
		src.TenantID, src.ID, src.Kind, src.Name, string(src.Config), src.Version, src.CreatedAt, src.UpdatedAt)
	if err != nil {
		return domain.Source{}, err
	}
	if ct.RowsAffected() == 0 {
		src, _, err := find() // lost the race: use the winner's row
		return src, err
	}
	if err := emitEntity(ctx, tx, tenantID, "", domain.EventSourceCreated, domain.EntitySource, src.ID, src); err != nil {
		return domain.Source{}, err
	}
	return src, nil
}

func (s *Store) ListSources(ctx context.Context, tenantID string) ([]domain.Source, error) {
	var out []domain.Source
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+sourceCols+` FROM sources WHERE tenant_id = $1 ORDER BY created_at, id`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			src, err := scanSource(rows)
			if err != nil {
				return err
			}
			out = append(out, src)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Store) CreateSource(ctx context.Context, tenantID, actorID string, in domain.CreateSourceInput) (*domain.Source, error) {
	src, err := store.NewSource(tenantID, in, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	err = s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if err := requireRef(ctx, tx, "users", "ownerId", tenantID, src.OwnerID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO sources (tenant_id, id, kind, name, owner_id, config, consent_at, retention_days, version, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7,$8,$9,$10,$11)`,
			src.TenantID, src.ID, src.Kind, src.Name, src.OwnerID, string(src.Config), src.ConsentAt, src.RetentionDays,
			src.Version, src.CreatedAt, src.UpdatedAt); err != nil {
			return err
		}
		return emitEntity(ctx, tx, tenantID, actorID, domain.EventSourceCreated, domain.EntitySource, src.ID, src)
	})
	if err != nil {
		return nil, err
	}
	return &src, nil
}

func (s *Store) UpdateSource(ctx context.Context, tenantID, actorID, id string, patch map[string]any, expectedVersion *int) (*domain.Source, error) {
	now := time.Now().UTC()
	var updated *domain.Source
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		src, err := scanSource(tx.QueryRow(ctx, `SELECT `+sourceCols+` FROM sources WHERE id = $1 AND tenant_id = $2 FOR UPDATE`, id, tenantID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return notFoundOnBadUUID(err)
		}
		if expectedVersion != nil && *expectedVersion != src.Version {
			return domain.ErrConflict
		}
		if err := store.ApplySourcePatch(&src, patch, now); err != nil {
			return err
		}
		if err := requireRef(ctx, tx, "users", "ownerId", tenantID, src.OwnerID); err != nil {
			return err
		}
		src.Version++
		if err := tx.QueryRow(ctx, `
			UPDATE sources SET name=$2, owner_id=$3, config=$4::jsonb, consent_at=$5, retention_days=$6, disabled_at=$7, version=$8
			WHERE id=$1 AND tenant_id=$9 RETURNING updated_at`,
			id, src.Name, src.OwnerID, string(src.Config), src.ConsentAt, src.RetentionDays, src.DisabledAt, src.Version, tenantID).
			Scan(&src.UpdatedAt); err != nil {
			return err
		}
		updated = &src
		return emitEntity(ctx, tx, tenantID, actorID, domain.EventSourceUpdated, domain.EntitySource, id, src)
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// DeleteSource returns how many signals the cascade took with the source —
// counted in the same transaction that deletes them, so the caller's audit row
// is the exact size of the deletion, not an estimate from before it. The
// manual source is refused (store.ErrManualSourceDelete).
func (s *Store) DeleteSource(ctx context.Context, tenantID, actorID, id string) (int, error) {
	var n int
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var kind string
		err := tx.QueryRow(ctx, `SELECT kind FROM sources WHERE id = $1 AND tenant_id = $2 FOR UPDATE`, id, tenantID).Scan(&kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return notFoundOnBadUUID(err)
		}
		if kind == domain.SourceManual {
			return store.ErrManualSourceDelete()
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM signals WHERE tenant_id = $1 AND source_id = $2`, tenantID, id).Scan(&n); err != nil {
			return err
		}
		// signals (and through them bodies + participants) cascade; origins keep their snapshot
		ct, err := tx.Exec(ctx, `DELETE FROM sources WHERE id = $1 AND tenant_id = $2`, id, tenantID)
		if err != nil {
			return notFoundOnBadUUID(err)
		}
		if ct.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return emitEntity(ctx, tx, tenantID, actorID, domain.EventSourceDeleted, domain.EntitySource, id, nil)
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// ---- signals ------------------------------------------------------------------

// signalCols deliberately never includes the body — it is another table.
const signalCols = `id::text, tenant_id::text, source_id::text, external_id, kind, title, excerpt, body_ref,
	occurred_at, captured_by::text, stage, snoozed_until, disposition_at, extracted, project_hint::text,
	retention_until, version, created_at, updated_at`

func scanSignal(row pgx.Row) (domain.Signal, error) {
	var s domain.Signal
	err := row.Scan(&s.ID, &s.TenantID, &s.SourceID, &s.ExternalID, &s.Kind, &s.Title, &s.Excerpt, &s.BodyRef,
		&s.OccurredAt, &s.CapturedBy, &s.Stage, &s.SnoozedUntil, &s.DispositionAt, &s.Extracted, &s.ProjectHint,
		&s.RetentionUntil, &s.Version, &s.CreatedAt, &s.UpdatedAt)
	s.Normalize()
	return s, err
}

// querySignals runs a full-column signals query, maps the rows and fills in
// each row's participants with one further query. The caller owns the
// WHERE/ORDER/LIMIT — and the tenant predicate that must be in it.
func querySignals(ctx context.Context, tx pgx.Tx, tenantID, q string, args ...any) ([]domain.Signal, error) {
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	var out []domain.Signal
	index := map[string]int{}
	for rows.Next() {
		sig, err := scanSignal(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		index[sig.ID] = len(out)
		out = append(out, sig)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	ids := make([]string, len(out))
	for i, sig := range out {
		ids[i] = sig.ID
	}
	prows, err := tx.Query(ctx, `
		SELECT signal_id::text, idx, name, email, role, person_id::text
		FROM signal_participants WHERE tenant_id = $1 AND signal_id = ANY($2::uuid[]) ORDER BY signal_id, idx`, tenantID, ids)
	if err != nil {
		return nil, err
	}
	defer prows.Close()
	for prows.Next() {
		var sid string
		var p domain.Participant
		if err := prows.Scan(&sid, &p.Idx, &p.Name, &p.Email, &p.Role, &p.PersonID); err != nil {
			return nil, err
		}
		if i, ok := index[sid]; ok {
			out[i].Participants = append(out[i].Participants, p)
		}
	}
	return out, prows.Err()
}

func getSignalTx(ctx context.Context, tx pgx.Tx, tenantID, id string, forUpdate bool) (*domain.Signal, error) {
	q := `SELECT ` + signalCols + ` FROM signals WHERE id = $2 AND tenant_id = $1`
	if forUpdate {
		q += ` FOR UPDATE`
	}
	sigs, err := querySignals(ctx, tx, tenantID, q, tenantID, id)
	if err != nil {
		return nil, notFoundOnBadUUID(err) // a non-uuid id is "not found"
	}
	if len(sigs) == 0 {
		return nil, domain.ErrNotFound
	}
	return &sigs[0], nil
}

func (s *Store) CreateSignal(ctx context.Context, tenantID, actorID string, in domain.CreateSignalInput) (*domain.Signal, error) {
	var created *domain.Signal
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var src domain.Source
		if in.SourceID == nil {
			got, err := manualSourceTx(ctx, tx, tenantID)
			if err != nil {
				return err
			}
			src = got
		} else {
			got, err := scanSource(tx.QueryRow(ctx, `SELECT `+sourceCols+` FROM sources WHERE id = $1 AND tenant_id = $2`, *in.SourceID, tenantID))
			if err != nil {
				return domain.Invalid("sourceId", "not found in this workspace")
			}
			src = got
		}
		sig, body, err := store.NewSignal(tenantID, actorID, src, in, time.Now().UTC())
		if err != nil {
			return err
		}
		if err := requireRef(ctx, tx, "projects", "projectHint", tenantID, sig.ProjectHint); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO signals (tenant_id, id, source_id, external_id, kind, title, excerpt, body_ref, occurred_at, captured_by,
				stage, extracted, project_hint, retention_until, version, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb,$13,$14,$15,$16,$17)`,
			sig.TenantID, sig.ID, sig.SourceID, sig.ExternalID, sig.Kind, sig.Title, sig.Excerpt, sig.BodyRef, sig.OccurredAt, sig.CapturedBy,
			string(sig.Stage), string(sig.Extracted), sig.ProjectHint, sig.RetentionUntil, sig.Version, sig.CreatedAt, sig.UpdatedAt); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation on (tenant, source, external_id)
				return domain.Invalid("externalId", "already captured from this source")
			}
			return err
		}
		if body != nil {
			if _, err := tx.Exec(ctx, `INSERT INTO signal_bodies (tenant_id, signal_id, body, fetched_at) VALUES ($1,$2,$3,$4)`,
				body.TenantID, body.SignalID, body.Body, body.FetchedAt); err != nil {
				return err
			}
		}
		for _, p := range sig.Participants {
			if _, err := tx.Exec(ctx, `
				INSERT INTO signal_participants (tenant_id, signal_id, idx, name, email, role, person_id)
				VALUES ($1,$2,$3,$4,$5,$6,$7)`, tenantID, sig.ID, p.Idx, p.Name, p.Email, p.Role, p.PersonID); err != nil {
				return err
			}
		}
		created = &sig
		return emitEntity(ctx, tx, tenantID, actorID, domain.EventSignalCreated, domain.EntitySignal, sig.ID, sig.Meta())
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

func (s *Store) GetSignal(ctx context.Context, tenantID, id string) (*domain.Signal, error) {
	var out *domain.Signal
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		sig, err := getSignalTx(ctx, tx, tenantID, id, false)
		if err != nil {
			return err
		}
		out = sig
		return nil
	})
	return out, err
}

// signalFilterWhere renders a SignalFilter as ` AND …` clauses with
// placeholders numbered from $2 ($1 is always tenant_id), plus their args.
func signalFilterWhere(f store.SignalFilter) (string, []any) {
	extra := ""
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		extra += " AND " + fmt.Sprintf(cond, len(args)+1)
	}
	if f.Stage != nil {
		add("stage = $%d", string(*f.Stage))
	}
	if f.SourceID != nil {
		add("source_id = $%d", *f.SourceID)
	}
	if f.ProjectHint != nil {
		add("project_hint = $%d", *f.ProjectHint)
	}
	if f.OccurredAfter != nil {
		add("occurred_at > $%d", *f.OccurredAfter)
	}
	return extra, args
}

// ListSignals is the keyset-paged read on (occurred_at, id) — see
// ListTasksPaged; keysetWhere was written for exactly this call.
func (s *Store) ListSignals(ctx context.Context, tenantID string, f store.SignalFilter, p store.Page) (store.PageResult[domain.Signal], error) {
	cur, err := store.DecodeCursor(p.Cursor)
	if err != nil {
		return store.PageResult[domain.Signal]{}, err
	}
	limit := p.EffectiveLimit()

	var out store.PageResult[domain.Signal]
	err = s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		extra, args := signalFilterWhere(f)
		after, cursorArgs := keysetWhere(cur, "occurred_at", len(args)+2)
		args = append(args, cursorArgs...)
		args = append(args, limit+1)
		q := `SELECT ` + signalCols + ` FROM signals WHERE tenant_id = $1` + extra + after +
			fmt.Sprintf(` ORDER BY occurred_at DESC, id DESC LIMIT $%d`, len(args)+1)
		rows, err := querySignals(ctx, tx, tenantID, q, append([]any{tenantID}, args...)...)
		if err != nil {
			return emptyOnBadUUID(err) // a non-uuid sourceId / projectHint matches nothing
		}
		out = store.Paginate(rows, limit, store.SignalKey)
		return nil
	})
	if out.Items == nil {
		out.Items = []domain.Signal{}
	}
	return out, err
}

func (s *Store) CountSignals(ctx context.Context, tenantID string, f store.SignalFilter) (int, error) {
	var n int
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		extra, args := signalFilterWhere(f)
		err := tx.QueryRow(ctx, `SELECT count(*) FROM signals WHERE tenant_id = $1`+extra, append([]any{tenantID}, args...)...).Scan(&n)
		return emptyOnBadUUID(err)
	})
	return n, err
}

// PruneSignals deletes every signal whose retention_until has passed — the
// sweep that makes a source's retentionDays a promise the server keeps. Its
// body and participants cascade (0014); work_item_signals rows keep their
// snapshot by design. It walks tenants through accounts (the one tenant-keyed
// table outside RLS) and deletes inside withTenant, so it works under the
// restricted app role as well as a superuser. No event is emitted: like a
// cascade, a retention delete is housekeeping, not a user action — but each
// tenant that lost rows gets one audit row, written in the same transaction as
// the delete, saying how many (and nothing about them).
func (s *Store) PruneSignals(ctx context.Context, now time.Time) (int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT tenant_id::text FROM accounts`)
	if err != nil {
		return 0, err
	}
	tenants, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	var total int64
	for _, tenantID := range tenants {
		err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
			ct, err := tx.Exec(ctx,
				`DELETE FROM signals WHERE tenant_id = $1 AND retention_until IS NOT NULL AND retention_until < $2`, tenantID, now)
			if err != nil {
				return err
			}
			if ct.RowsAffected() == 0 {
				return nil
			}
			total += ct.RowsAffected()
			return appendAuditTx(ctx, tx, tenantID, store.PruneAuditEntry(int(ct.RowsAffected())))
		})
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func (s *Store) UpdateSignalDisposition(ctx context.Context, tenantID, actorID, id string, patch map[string]any, expectedVersion *int) (*domain.Signal, error) {
	now := time.Now().UTC()
	var updated *domain.Signal
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		existing, err := getSignalTx(ctx, tx, tenantID, id, true)
		if err != nil {
			return err
		}
		if expectedVersion != nil && *expectedVersion != existing.Version {
			return domain.ErrConflict
		}
		next := *existing
		if err := store.ApplySignalPatch(&next, patch, now); err != nil {
			return err
		}
		if err := requireRef(ctx, tx, "projects", "projectHint", tenantID, next.ProjectHint); err != nil {
			return err
		}
		next.Version = existing.Version + 1
		// Only the disposition columns are written — the signals_immutable
		// trigger would reject anything else, and ApplySignalPatch already did.
		if err := tx.QueryRow(ctx, `
			UPDATE signals SET stage=$2, snoozed_until=$3, disposition_at=$4, extracted=$5::jsonb, project_hint=$6, retention_until=$7, version=$8
			WHERE id=$1 AND tenant_id=$9 RETURNING updated_at`,
			id, string(next.Stage), next.SnoozedUntil, next.DispositionAt, string(next.Extracted), next.ProjectHint, next.RetentionUntil, next.Version, tenantID).
			Scan(&next.UpdatedAt); err != nil {
			return err
		}
		updated = &next
		return emitEntity(ctx, tx, tenantID, actorID, domain.EventSignalUpdated, domain.EntitySignal, id, next.Meta())
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *Store) DeleteSignal(ctx context.Context, tenantID, actorID, id string) error {
	return s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		// body and participants cascade; work_item_signals rows stay (no FK) with their snapshot
		ct, err := tx.Exec(ctx, `DELETE FROM signals WHERE id = $1 AND tenant_id = $2`, id, tenantID)
		if err != nil {
			return notFoundOnBadUUID(err)
		}
		if ct.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return emitEntity(ctx, tx, tenantID, actorID, domain.EventSignalDeleted, domain.EntitySignal, id, nil)
	})
}

func (s *Store) GetSignalBody(ctx context.Context, tenantID, id string) (*domain.SignalBody, error) {
	var out *domain.SignalBody
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var createdAt time.Time
		if err := tx.QueryRow(ctx, `SELECT created_at FROM signals WHERE id = $1 AND tenant_id = $2`, id, tenantID).Scan(&createdAt); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrNotFound
			}
			return notFoundOnBadUUID(err)
		}
		b := domain.SignalBody{TenantID: tenantID, SignalID: id, FetchedAt: createdAt}
		err := tx.QueryRow(ctx, `SELECT body, fetched_at FROM signal_bodies WHERE signal_id = $1 AND tenant_id = $2`, id, tenantID).
			Scan(&b.Body, &b.FetchedAt)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		out = &b
		return nil
	})
	return out, err
}

// ---- origins ------------------------------------------------------------------

const originCols = `tenant_id::text, task_id::text, signal_id::text, origin_snapshot, created_at`

func scanOrigin(row pgx.Row) (domain.Origin, error) {
	var o domain.Origin
	var snap []byte
	if err := row.Scan(&o.TenantID, &o.TaskID, &o.SignalID, &snap, &o.CreatedAt); err != nil {
		return o, err
	}
	return o, unmarshalInto(snap, &o.Snapshot)
}

func (s *Store) AttachSignalToTask(ctx context.Context, tenantID, actorID, taskID, signalID string) (*domain.Origin, error) {
	now := time.Now().UTC()
	var out *domain.Origin
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := getTaskTx(ctx, tx, tenantID, taskID, false); err != nil {
			return err
		}
		sig, err := getSignalTx(ctx, tx, tenantID, signalID, true)
		if errors.Is(err, domain.ErrNotFound) {
			return domain.Invalid("signalId", "not found in this workspace")
		}
		if err != nil {
			return err
		}
		origin := store.NewOrigin(taskID, *sig, now)
		ct, err := tx.Exec(ctx, `
			INSERT INTO work_item_signals (tenant_id, task_id, signal_id, origin_snapshot, created_at)
			VALUES ($1,$2,$3,$4::jsonb,$5) ON CONFLICT DO NOTHING`,
			tenantID, taskID, signalID, jsonObj(origin.Snapshot), origin.CreatedAt)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 { // idempotent: already attached, nothing to emit
			existing, err := scanOrigin(tx.QueryRow(ctx,
				`SELECT `+originCols+` FROM work_item_signals WHERE tenant_id = $1 AND task_id = $2 AND signal_id = $3`, tenantID, taskID, signalID))
			if err != nil {
				return err
			}
			out = &existing
			return nil
		}
		// an inbox/snoozed signal is now disposed: attached
		if sig.Stage == domain.SignalInbox || sig.Stage == domain.SignalSnoozed {
			sig.Stage, sig.SnoozedUntil, sig.DispositionAt, sig.Version = domain.SignalAttached, nil, &now, sig.Version+1
			if err := tx.QueryRow(ctx, `
				UPDATE signals SET stage=$2, snoozed_until=NULL, disposition_at=$3, version=$4
				WHERE id=$1 AND tenant_id=$5 RETURNING updated_at`,
				signalID, string(sig.Stage), now, sig.Version, tenantID).Scan(&sig.UpdatedAt); err != nil {
				return err
			}
			if err := emitEntity(ctx, tx, tenantID, actorID, domain.EventSignalUpdated, domain.EntitySignal, signalID, sig.Meta()); err != nil {
				return err
			}
		}
		out = &origin
		return emitEntity(ctx, tx, tenantID, actorID, domain.EventOriginAttached, domain.EntityOrigin, taskID, origin)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) DetachSignalFromTask(ctx context.Context, tenantID, actorID, taskID, signalID string) error {
	return s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := getTaskTx(ctx, tx, tenantID, taskID, false); err != nil {
			return err
		}
		ct, err := tx.Exec(ctx, `DELETE FROM work_item_signals WHERE tenant_id = $1 AND task_id = $2 AND signal_id = $3`, tenantID, taskID, signalID)
		if err != nil {
			return notFoundOnBadUUID(err)
		}
		if ct.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return emitEntity(ctx, tx, tenantID, actorID, domain.EventOriginDetached, domain.EntityOrigin, taskID,
			map[string]string{"taskId": taskID, "signalId": signalID})
	})
}

func (s *Store) ListTaskOrigins(ctx context.Context, tenantID, taskID string) ([]domain.Origin, error) {
	out := []domain.Origin{}
	err := s.withTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := getTaskTx(ctx, tx, tenantID, taskID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+originCols+` FROM work_item_signals WHERE tenant_id = $1 AND task_id = $2 ORDER BY created_at, signal_id`, tenantID, taskID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			o, err := scanOrigin(rows)
			if err != nil {
				return err
			}
			out = append(out, o)
		}
		return rows.Err()
	})
	return out, err
}
