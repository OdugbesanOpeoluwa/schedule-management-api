package model

import "time"

type User struct {
	ID           string
	Email        string
	PasswordHash string
	Name         string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type Appointment struct {
	ID          string
	Title       string
	Description string
	StartTime   time.Time
	EndTime     time.Time
	UserID      string
	Status      string
	Location    string
	AttendeeIDs []string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
