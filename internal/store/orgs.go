package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Role is a member's standing in an org.
type Role string

// Roles.
const (
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

// Org is a tenant.
type Org struct {
	ID          uuid.UUID
	Name        string
	PersonalFor string // subject whose personal org this is, "" for a created one
	CreatedAt   time.Time
}

// User is a human who has signed in at least once.
type User struct {
	ID            string
	Email         string
	Name          string
	PersonalOrgID uuid.UUID
	ActiveOrgID   uuid.UUID
	CreatedAt     time.Time
	LastSeenAt    time.Time
}

// Member is a user's membership of one org.
type Member struct {
	OrgID    uuid.UUID
	UserID   string
	Email    string
	Name     string
	Role     Role
	JoinedAt time.Time
}

// Invite is a magic link into an org.
type Invite struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	Role      Role
	CreatedBy string
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    *time.Time
	UsedBy    string
}

// Standing is what a sign-in resolves to: the user, their active org and
// their role in it.
type Standing struct {
	User *User
	Org  *Org
	Role Role
}

// Org errors.
var (
	ErrInviteInvalid = errors.New("store: invite is invalid, used or expired")
	ErrLastAdmin     = errors.New("store: the org needs at least one admin")
	ErrPersonalOrg   = errors.New("store: cannot leave a personal org")
	ErrAlreadyMember = errors.New("store: already a member")
	ErrNotMember     = errors.New("store: not a member of this org")
	ErrNoStanding    = errors.New("store: user has no org")
)

// EnsureUser records a sign-in: creates the user and their personal org on
// first sight, otherwise bumps last_seen and refreshes email and name. A new
// user whose claimLegacy matches an unclaimed migrated org takes that org as
// their personal org and becomes its admin; otherwise a fresh org is made.
func (p *Postgres) EnsureUser(ctx context.Context, id, email, name, claimLegacy string) (*Standing, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var u User
	err = tx.QueryRow(ctx, `update users set email=$2, name=$3, last_seen_at=now() where id=$1
		returning id, email, name, personal_org_id, active_org_id, created_at, last_seen_at`, id, email, name).
		Scan(&u.ID, &u.Email, &u.Name, &u.PersonalOrgID, &u.ActiveOrgID, &u.CreatedAt, &u.LastSeenAt)
	if errors.Is(err, pgx.ErrNoRows) {
		var orgID uuid.UUID
		claimed := false
		if claimLegacy != "" {
			err = tx.QueryRow(ctx, `update orgs set personal_for=$2, name=$3 where legacy_owner=$1 and personal_for is null
				and not exists (select 1 from org_members m where m.org_id = orgs.id) returning id`, claimLegacy, id, orgName(email, name)).Scan(&orgID)
			switch {
			case err == nil:
				claimed = true
			case errors.Is(err, pgx.ErrNoRows):
			default:
				return nil, fmt.Errorf("store: %w", err)
			}
		}
		if !claimed {
			orgID = uuid.New()
			if _, err := tx.Exec(ctx, `insert into orgs (id, name, personal_for) values ($1,$2,$3)`, orgID, orgName(email, name), id); err != nil {
				return nil, fmt.Errorf("store: %w", err)
			}
		}
		if err := tx.QueryRow(ctx, `insert into users (id, email, name, personal_org_id, active_org_id) values ($1,$2,$3,$4,$4)
			returning id, email, name, personal_org_id, active_org_id, created_at, last_seen_at`, id, email, name, orgID).
			Scan(&u.ID, &u.Email, &u.Name, &u.PersonalOrgID, &u.ActiveOrgID, &u.CreatedAt, &u.LastSeenAt); err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
		if _, err := tx.Exec(ctx, `insert into org_members (org_id, user_id, role) values ($1,$2,'admin')`, orgID, id); err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
		if claimed {
			if _, err := tx.Exec(ctx, `update api_keys set created_by=$2 where org_id=$1::text and created_by is null`, orgID, id); err != nil {
				return nil, fmt.Errorf("store: %w", err)
			}
		}
	} else if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	st, err := standing(ctx, tx, &u)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	return st, nil
}

func orgName(email, name string) string {
	if name != "" {
		return name + "'s org"
	}
	if email != "" {
		return email
	}
	return "Personal"
}

// GetStanding returns a user's active org and role.
func (p *Postgres) GetStanding(ctx context.Context, userID string) (*Standing, error) {
	var u User
	err := p.pool.QueryRow(ctx, `select id, email, name, personal_org_id, active_org_id, created_at, last_seen_at from users where id=$1`, userID).
		Scan(&u.ID, &u.Email, &u.Name, &u.PersonalOrgID, &u.ActiveOrgID, &u.CreatedAt, &u.LastSeenAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoStanding
	}
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	return standing(ctx, p.pool, &u)
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func standing(ctx context.Context, q querier, u *User) (*Standing, error) {
	var o Org
	var personal *string
	var role Role
	err := q.QueryRow(ctx, `select o.id, o.name, o.personal_for, o.created_at, m.role from orgs o
		join org_members m on m.org_id = o.id and m.user_id = $2 where o.id = $1`, u.ActiveOrgID, u.ID).
		Scan(&o.ID, &o.Name, &personal, &o.CreatedAt, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoStanding
	}
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if personal != nil {
		o.PersonalFor = *personal
	}
	return &Standing{User: u, Org: &o, Role: role}, nil
}

// MemberRole returns a user's role in an org, or ErrNotMember.
func (p *Postgres) MemberRole(ctx context.Context, orgID uuid.UUID, userID string) (Role, error) {
	var role Role
	err := p.pool.QueryRow(ctx, `select role from org_members where org_id=$1 and user_id=$2`, orgID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotMember
	}
	if err != nil {
		return "", fmt.Errorf("store: %w", err)
	}
	return role, nil
}

// GetOrg fetches one org.
func (p *Postgres) GetOrg(ctx context.Context, orgID uuid.UUID) (*Org, error) {
	var o Org
	var personal *string
	err := p.pool.QueryRow(ctx, `select id, name, personal_for, created_at from orgs where id=$1`, orgID).Scan(&o.ID, &o.Name, &personal, &o.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if personal != nil {
		o.PersonalFor = *personal
	}
	return &o, nil
}

// RenameOrg sets an org's name.
func (p *Postgres) RenameOrg(ctx context.Context, orgID uuid.UUID, name string) error {
	_, err := p.pool.Exec(ctx, `update orgs set name=$2 where id=$1`, orgID, name)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

// ListMembers returns an org's members, admins first then by join time.
func (p *Postgres) ListMembers(ctx context.Context, orgID uuid.UUID) ([]*Member, error) {
	rows, err := p.pool.Query(ctx, `select m.org_id, m.user_id, u.email, u.name, m.role, m.joined_at from org_members m
		join users u on u.id = m.user_id where m.org_id=$1 order by (m.role='admin') desc, m.joined_at`, orgID)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	defer rows.Close()
	var out []*Member
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.OrgID, &m.UserID, &m.Email, &m.Name, &m.Role, &m.JoinedAt); err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

// SetMemberRole changes a member's role, refusing to demote the last admin.
func (p *Postgres) SetMemberRole(ctx context.Context, orgID uuid.UUID, userID string, role Role) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if role != RoleAdmin {
		if err := lastAdminGuard(ctx, tx, orgID, userID); err != nil {
			return err
		}
	}
	tag, err := tx.Exec(ctx, `update org_members set role=$3 where org_id=$1 and user_id=$2`, orgID, userID, role)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotMember
	}
	return tx.Commit(ctx)
}

// RemoveMember drops a user from an org (by an admin, or themselves leaving).
// The user's active org falls back to their personal org. Keys they created
// in the org are revoked. The last admin cannot be removed, nor can anyone
// be removed from their own personal org.
func (p *Postgres) RemoveMember(ctx context.Context, orgID uuid.UUID, userID string) (*Standing, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var personal uuid.UUID
	if err := tx.QueryRow(ctx, `select personal_org_id from users where id=$1`, userID).Scan(&personal); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotMember
		}
		return nil, fmt.Errorf("store: %w", err)
	}
	if personal == orgID {
		return nil, ErrPersonalOrg
	}
	if err := lastAdminGuard(ctx, tx, orgID, userID); err != nil {
		return nil, err
	}
	tag, err := tx.Exec(ctx, `delete from org_members where org_id=$1 and user_id=$2`, orgID, userID)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotMember
	}
	if _, err := tx.Exec(ctx, `update api_keys set revoked_at=now() where org_id=$1::text and created_by=$2 and revoked_at is null`, orgID, userID); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	var u User
	if err := tx.QueryRow(ctx, `update users set active_org_id = personal_org_id where id=$1 and active_org_id=$2
		returning id, email, name, personal_org_id, active_org_id, created_at, last_seen_at`, userID, orgID).
		Scan(&u.ID, &u.Email, &u.Name, &u.PersonalOrgID, &u.ActiveOrgID, &u.CreatedAt, &u.LastSeenAt); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("store: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	return p.GetStanding(ctx, userID)
}

// lastAdminGuard fails when userID is the only admin of orgID.
func lastAdminGuard(ctx context.Context, q querier, orgID uuid.UUID, userID string) error {
	var others int
	if err := q.QueryRow(ctx, `select count(*) from org_members where org_id=$1 and role='admin' and user_id<>$2`, orgID, userID).Scan(&others); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	var mine Role
	if err := q.QueryRow(ctx, `select role from org_members where org_id=$1 and user_id=$2`, orgID, userID).Scan(&mine); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotMember
		}
		return fmt.Errorf("store: %w", err)
	}
	if mine == RoleAdmin && others == 0 {
		return ErrLastAdmin
	}
	return nil
}

// ---- invites ------------------------------------------------------------

// CreateInvite stores an invite; the caller holds the plaintext token.
func (p *Postgres) CreateInvite(ctx context.Context, inv *Invite, tokenHash []byte) error {
	_, err := p.pool.Exec(ctx, `insert into org_invites (id, org_id, token_hash, role, created_by, expires_at) values ($1,$2,$3,$4,$5,$6)`,
		inv.ID, inv.OrgID, tokenHash, inv.Role, inv.CreatedBy, inv.ExpiresAt)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

// ListInvites returns an org's invites, newest first.
func (p *Postgres) ListInvites(ctx context.Context, orgID uuid.UUID) ([]*Invite, error) {
	rows, err := p.pool.Query(ctx, `select id, org_id, role, created_by, created_at, expires_at, used_at, coalesce(used_by,'') from org_invites where org_id=$1 order by created_at desc`, orgID)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	defer rows.Close()
	var out []*Invite
	for rows.Next() {
		var i Invite
		if err := rows.Scan(&i.ID, &i.OrgID, &i.Role, &i.CreatedBy, &i.CreatedAt, &i.ExpiresAt, &i.UsedAt, &i.UsedBy); err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
		out = append(out, &i)
	}
	return out, rows.Err()
}

// DeleteInvite revokes an unused invite.
func (p *Postgres) DeleteInvite(ctx context.Context, orgID, id uuid.UUID) error {
	tag, err := p.pool.Exec(ctx, `delete from org_invites where org_id=$1 and id=$2`, orgID, id)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// PeekInvite describes an invite by token without consuming it.
func (p *Postgres) PeekInvite(ctx context.Context, tokenHash []byte) (*Invite, *Org, error) {
	var i Invite
	var o Org
	var personal *string
	err := p.pool.QueryRow(ctx, `select i.id, i.org_id, i.role, i.created_by, i.created_at, i.expires_at, i.used_at, coalesce(i.used_by,''), o.name, o.personal_for, o.created_at
		from org_invites i join orgs o on o.id = i.org_id where i.token_hash=$1 and i.used_at is null and i.expires_at > now()`, tokenHash).
		Scan(&i.ID, &i.OrgID, &i.Role, &i.CreatedBy, &i.CreatedAt, &i.ExpiresAt, &i.UsedAt, &i.UsedBy, &o.Name, &personal, &o.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrInviteInvalid
	}
	if err != nil {
		return nil, nil, fmt.Errorf("store: %w", err)
	}
	o.ID = i.OrgID
	if personal != nil {
		o.PersonalFor = *personal
	}
	return &i, &o, nil
}

// AcceptInvite consumes an invite for userID: adds the membership (or keeps
// an existing one) and makes that org the user's active org.
func (p *Postgres) AcceptInvite(ctx context.Context, tokenHash []byte, userID string) (*Standing, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var orgID uuid.UUID
	var role Role
	err = tx.QueryRow(ctx, `update org_invites set used_at=now(), used_by=$2 where token_hash=$1 and used_at is null and expires_at > now() returning org_id, role`, tokenHash, userID).Scan(&orgID, &role)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInviteInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if _, err := tx.Exec(ctx, `insert into org_members (org_id, user_id, role) values ($1,$2,$3) on conflict (org_id, user_id) do nothing`, orgID, userID, role); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if _, err := tx.Exec(ctx, `update users set active_org_id=$2 where id=$1`, userID, orgID); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	return p.GetStanding(ctx, userID)
}

// SwitchOrg makes one of the user's orgs active.
func (p *Postgres) SwitchOrg(ctx context.Context, userID string, orgID uuid.UUID) (*Standing, error) {
	tag, err := p.pool.Exec(ctx, `update users u set active_org_id=$2 where u.id=$1 and exists (select 1 from org_members m where m.org_id=$2 and m.user_id=$1)`, userID, orgID)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotMember
	}
	return p.GetStanding(ctx, userID)
}

// ListUserOrgs returns every org the user belongs to with their role.
func (p *Postgres) ListUserOrgs(ctx context.Context, userID string) ([]*Member, map[uuid.UUID]*Org, error) {
	rows, err := p.pool.Query(ctx, `select m.org_id, m.role, m.joined_at, o.name, coalesce(o.personal_for,''), o.created_at from org_members m
		join orgs o on o.id = m.org_id where m.user_id=$1 order by (o.personal_for = $1) desc, m.joined_at`, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("store: %w", err)
	}
	defer rows.Close()
	var ms []*Member
	orgs := map[uuid.UUID]*Org{}
	for rows.Next() {
		m := &Member{UserID: userID}
		o := &Org{}
		if err := rows.Scan(&m.OrgID, &m.Role, &m.JoinedAt, &o.Name, &o.PersonalFor, &o.CreatedAt); err != nil {
			return nil, nil, fmt.Errorf("store: %w", err)
		}
		o.ID = m.OrgID
		ms = append(ms, m)
		orgs[o.ID] = o
	}
	return ms, orgs, rows.Err()
}
