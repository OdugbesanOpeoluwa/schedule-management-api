package handler

import (
	pb "schedule-management-api/gen/appointment/v1"
	"schedule-management-api/internal/store"
)

type Handler struct {
	pb.UnimplementedScheduleServiceServer
	store  *store.Store
	secret string
}

func New(st *store.Store, secret string) *Handler {
	return &Handler{store: st, secret: secret}
}
