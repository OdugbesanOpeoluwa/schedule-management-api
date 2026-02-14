package store

import (
	"context"

	"schedule-management-api/internal/model"
)

func (s *Store) CreateUser(ctx context.Context, u *model.User) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO users (id, email, password_hash, name) VALUES ($1,$2,$3,$4)`,
		u.ID, u.Email, u.PasswordHash, u.Name,
	)
	return err
}

func (s *Store) UserByEmail(ctx context.Context, email string) (*model.User, error) {
	u := &model.User{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, email, password_hash, name, created_at, updated_at
		 FROM users WHERE email = $1`, email,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Name, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}
