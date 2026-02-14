package store

import (
	"context"
	"time"

	"schedule-management-api/internal/model"
)

func (s *Store) CreateAppointment(ctx context.Context, a *model.Appointment) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx,
		`INSERT INTO appointments (id,title,description,start_time,end_time,user_id,status,location)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		a.ID, a.Title, a.Description, a.StartTime, a.EndTime, a.UserID, a.Status, a.Location,
	)
	if err != nil {
		return err
	}

	for _, uid := range a.AttendeeIDs {
		_, err = tx.Exec(ctx,
			`INSERT INTO appointment_attendees (appointment_id, user_id) VALUES ($1,$2)`,
			a.ID, uid,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

func (s *Store) HasOverlap(ctx context.Context, userID string, start, end time.Time, excludeID string) (bool, error) {
	q := `SELECT EXISTS(
		SELECT 1 FROM appointments
		WHERE user_id = $1
		  AND status = 'confirmed'
		  AND start_time < $3
		  AND end_time > $2`

	args := []any{userID, start, end}

	if excludeID != "" {
		q += ` AND id != $4`
		args = append(args, excludeID)
	}
	q += `)`

	var exists bool
	err := s.pool.QueryRow(ctx, q, args...).Scan(&exists)
	return exists, err
}

func (s *Store) ListAppointments(ctx context.Context, userID string, from, to time.Time) ([]model.Appointment, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, title, description, start_time, end_time,
		        user_id, status, location, created_at, updated_at
		 FROM appointments
		 WHERE user_id = $1
		   AND start_time >= $2 AND end_time <= $3
		   AND status = 'confirmed'
		 ORDER BY start_time`, userID, from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Appointment
	for rows.Next() {
		var a model.Appointment
		if err := rows.Scan(
			&a.ID, &a.Title, &a.Description, &a.StartTime, &a.EndTime,
			&a.UserID, &a.Status, &a.Location, &a.CreatedAt, &a.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) GetAppointment(ctx context.Context, id string) (*model.Appointment, error) {
	a := &model.Appointment{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, title, description, start_time, end_time,
		        user_id, status, location, created_at, updated_at
		 FROM appointments WHERE id = $1`, id,
	).Scan(&a.ID, &a.Title, &a.Description, &a.StartTime, &a.EndTime,
		&a.UserID, &a.Status, &a.Location, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}

	// load attendees
	rows, err := s.pool.Query(ctx,
		`SELECT user_id FROM appointment_attendees WHERE appointment_id = $1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		a.AttendeeIDs = append(a.AttendeeIDs, uid)
	}
	return a, rows.Err()
}

func (s *Store) UpdateAppointment(ctx context.Context, a *model.Appointment) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx,
		`UPDATE appointments
		 SET title=$1, description=$2, start_time=$3, end_time=$4, location=$5, updated_at=NOW()
		 WHERE id=$6 AND user_id=$7`,
		a.Title, a.Description, a.StartTime, a.EndTime, a.Location, a.ID, a.UserID,
	)
	if err != nil {
		return err
	}

	// replace attendees
	_, _ = tx.Exec(ctx, `DELETE FROM appointment_attendees WHERE appointment_id=$1`, a.ID)
	for _, uid := range a.AttendeeIDs {
		_, err = tx.Exec(ctx,
			`INSERT INTO appointment_attendees (appointment_id, user_id) VALUES ($1,$2)`,
			a.ID, uid,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

func (s *Store) DeleteAppointment(ctx context.Context, id, userID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE appointments SET status='cancelled', updated_at=NOW()
		 WHERE id=$1 AND user_id=$2`, id, userID,
	)
	return err
}
