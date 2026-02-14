package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type RefreshToken struct {
	ID         string
	UserID     string
	TokenHash  string
	ExpiresAt  time.Time
	Revoked    bool
	ReplacedBy *string
	CreatedAt  time.Time
}

func (s *Store) CreateRefreshToken(ctx context.Context, userID, tokenHash string, expiresAt time.Time) (string, error) {
	id := uuid.New().String()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at) VALUES ($1,$2,$3,$4)`,
		id, userID, tokenHash, expiresAt,
	)
	return id, err
}

func (s *Store) GetRefreshTokenByHash(ctx context.Context, tokenHash string) (*RefreshToken, error) {
	rt := &RefreshToken{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, user_id, token_hash, expires_at, revoked, replaced_by, created_at
		 FROM refresh_tokens WHERE token_hash = $1`, tokenHash,
	).Scan(&rt.ID, &rt.UserID, &rt.TokenHash, &rt.ExpiresAt, &rt.Revoked, &rt.ReplacedBy, &rt.CreatedAt)
	if err != nil {
		return nil, err
	}
	return rt, nil
}

// rotate: revoke old token, create new one, link them
func (s *Store) RotateRefreshToken(ctx context.Context, oldID, newID, userID, newHash string, newExpiry time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// revoke old, point to replacement
	_, err = tx.Exec(ctx,
		`UPDATE refresh_tokens SET revoked = true, replaced_by = $1 WHERE id = $2`,
		newID, oldID,
	)
	if err != nil {
		return err
	}

	// insert new
	_, err = tx.Exec(ctx,
		`INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at) VALUES ($1,$2,$3,$4)`,
		newID, userID, newHash, newExpiry,
	)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// revoke all tokens for a user (on logout or suspected theft)
func (s *Store) RevokeAllRefreshTokens(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE refresh_tokens SET revoked = true WHERE user_id = $1 AND revoked = false`,
		userID,
	)
	return err
}
