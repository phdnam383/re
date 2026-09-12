package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"re/internal/rulemanagement"
)

type Repository struct{ db *sql.DB }

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

var _ rulemanagement.Repository = (*Repository)(nil)

const columns = `id::text, name, COALESCE(description, ''), rule_content, salience, enabled, created_at, updated_at`

type scanner interface{ Scan(...any) error }

func scan(row scanner) (rulemanagement.Rule, error) {
	var r rulemanagement.Rule
	err := row.Scan(&r.ID, &r.Name, &r.Description, &r.Content, &r.Salience, &r.Enabled, &r.CreatedAt, &r.UpdatedAt)
	r.CreatedAt = r.CreatedAt.UTC()
	r.UpdatedAt = r.UpdatedAt.UTC()
	return r, mapError(err)
}

func mapError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return rulemanagement.ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return rulemanagement.ErrConflict
	}
	return err
}

func (r *Repository) Create(ctx context.Context, rule rulemanagement.Rule) (rulemanagement.Rule, error) {
	return scan(r.db.QueryRowContext(ctx, `INSERT INTO rca_rule (name, description, rule_content, salience, enabled) VALUES ($1,$2,$3,$4,$5) RETURNING `+columns,
		rule.Name, rule.Description, rule.Content, rule.Salience, rule.Enabled))
}
func (r *Repository) Get(ctx context.Context, id string) (rulemanagement.Rule, error) {
	return scan(r.db.QueryRowContext(ctx, `SELECT `+columns+` FROM rca_rule WHERE id=$1`, id))
}
func (r *Repository) Update(ctx context.Context, rule rulemanagement.Rule, expected time.Time) (rulemanagement.Rule, error) {
	result, err := scan(r.db.QueryRowContext(ctx, `UPDATE rca_rule SET name=$2, description=$3, rule_content=$4, salience=$5, enabled=$6,
 updated_at=GREATEST(clock_timestamp(), updated_at + interval '1 microsecond') WHERE id=$1 AND updated_at=$7 RETURNING `+columns,
		rule.ID, rule.Name, rule.Description, rule.Content, rule.Salience, rule.Enabled, expected))
	if errors.Is(err, rulemanagement.ErrNotFound) {
		if _, getErr := r.Get(ctx, rule.ID); getErr != nil {
			return rulemanagement.Rule{}, getErr
		}
		return rulemanagement.Rule{}, rulemanagement.ErrConflict
	}
	return result, err
}
func (r *Repository) Delete(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM rca_rule WHERE id=$1`, id)
	if err != nil {
		return mapError(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return rulemanagement.ErrNotFound
	}
	return nil
}
func (r *Repository) List(ctx context.Context, filter rulemanagement.ListFilter) (rulemanagement.RulePage, error) {
	page := rulemanagement.RulePage{Items: []rulemanagement.Rule{}}
	// Keep count and page consistent even when another request changes rules.
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	const where = ` FROM rca_rule WHERE ($1::boolean IS NULL OR enabled=$1)`
	if err := tx.QueryRowContext(ctx, `SELECT count(*)`+where, filter.Enabled).Scan(&page.Total); err != nil {
		return page, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+columns+where+` ORDER BY salience DESC, name ASC, id ASC LIMIT $2 OFFSET $3`, filter.Enabled, filter.Limit, filter.Offset)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		rule, err := scan(rows)
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, rule)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if err := rows.Close(); err != nil {
		return page, err
	}
	return page, tx.Commit()
}
