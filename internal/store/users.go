package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// User is a human who has signed in at least once.
type User struct {
	ID         string // the identity provider's subject
	Email      string
	Name       string
	CreatedAt  time.Time
	LastSeenAt time.Time
}

const userCols = `id, email, name, created_at, last_seen_at`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.CreatedAt, &u.LastSeenAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	return &u, nil
}

// EnsureUser records a sign-in: it creates the user on first sight, and
// otherwise bumps last_seen and refreshes email and name.
func (p *Postgres) EnsureUser(ctx context.Context, id, email, name string) (*User, error) {
	return scanUser(p.pool.QueryRow(ctx, `insert into users (id, email, name) values ($1,$2,$3)
		on conflict (id) do update set email=excluded.email, name=excluded.name, last_seen_at=now()
		returning `+userCols, id, email, name))
}

// GetUser fetches one user.
func (p *Postgres) GetUser(ctx context.Context, id string) (*User, error) {
	return scanUser(p.pool.QueryRow(ctx, `select `+userCols+` from users where id=$1`, id))
}

// ListUsers returns everyone who has signed in, by name then email.
func (p *Postgres) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := p.pool.Query(ctx, `select `+userCols+` from users order by lower(name), lower(email)`)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	defer rows.Close()
	out := []*User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
