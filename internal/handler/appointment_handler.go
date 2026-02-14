package handler

import (
	"context"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"schedule-management-api/internal/middleware"
	"schedule-management-api/internal/model"
	pb "schedule-management-api/gen/appointment/v1"
)

func uid(ctx context.Context) string {
	return ctx.Value(middleware.UserIDKey).(string)
}

func (h *Handler) CreateAppointment(ctx context.Context, req *pb.CreateAppointmentRequest) (*pb.CreateAppointmentResponse, error) {
	userID := uid(ctx)

	if req.Title == "" {
		return nil, status.Error(codes.InvalidArgument, "title required")
	}
	if req.StartTime == nil || req.EndTime == nil {
		return nil, status.Error(codes.InvalidArgument, "times required")
	}

	start := req.StartTime.AsTime()
	end := req.EndTime.AsTime()

	if !end.After(start) {
		return nil, status.Error(codes.InvalidArgument, "end must be after start")
	}
	if start.Before(time.Now().Add(-5 * time.Minute)) {
		return nil, status.Error(codes.InvalidArgument, "cannot book in the past")
	}

	// app-level overlap check
	if dup, err := h.store.HasOverlap(ctx, userID, start, end, ""); err != nil {
		return nil, status.Error(codes.Internal, "internal error")
	} else if dup {
		return nil, status.Error(codes.AlreadyExists, "time conflicts with existing appointment")
	}

	apt := &model.Appointment{
		ID:          uuid.New().String(),
		Title:       req.Title,
		Description: req.Description,
		StartTime:   start,
		EndTime:     end,
		UserID:      userID,
		Status:      "confirmed",
		Location:    req.Location,
		AttendeeIDs: req.AttendeeIds,
	}

	if err := h.store.CreateAppointment(ctx, apt); err != nil {
		// db exclusion constraint caught a race
		return nil, status.Error(codes.AlreadyExists, "time conflicts with existing appointment")
	}

	return &pb.CreateAppointmentResponse{Appointment: toProto(apt)}, nil
}

func (h *Handler) ListAppointments(ctx context.Context, req *pb.ListAppointmentsRequest) (*pb.ListAppointmentsResponse, error) {
	userID := uid(ctx)

	from := time.Now().AddDate(0, 0, -30)
	to := time.Now().AddDate(0, 2, 0)

	if req.RangeStart != nil {
		from = req.RangeStart.AsTime()
	}
	if req.RangeEnd != nil {
		to = req.RangeEnd.AsTime()
	}

	apts, err := h.store.ListAppointments(ctx, userID, from, to)
	if err != nil {
		return nil, status.Error(codes.Internal, "internal error")
	}

	out := make([]*pb.Appointment, len(apts))
	for i := range apts {
		out[i] = toProto(&apts[i])
	}
	return &pb.ListAppointmentsResponse{Appointments: out}, nil
}

func (h *Handler) GetAppointment(ctx context.Context, req *pb.GetAppointmentRequest) (*pb.GetAppointmentResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id required")
	}

	apt, err := h.store.GetAppointment(ctx, req.Id)
	if err != nil {
		return nil, status.Error(codes.NotFound, "not found")
	}

	// ownership — return 404 not 403 to hide existence
	if apt.UserID != uid(ctx) {
		return nil, status.Error(codes.NotFound, "not found")
	}

	return &pb.GetAppointmentResponse{Appointment: toProto(apt)}, nil
}

func (h *Handler) UpdateAppointment(ctx context.Context, req *pb.UpdateAppointmentRequest) (*pb.UpdateAppointmentResponse, error) {
	userID := uid(ctx)

	if req.Id == "" || req.Title == "" {
		return nil, status.Error(codes.InvalidArgument, "id and title required")
	}
	if req.StartTime == nil || req.EndTime == nil {
		return nil, status.Error(codes.InvalidArgument, "times required")
	}

	start := req.StartTime.AsTime()
	end := req.EndTime.AsTime()
	if !end.After(start) {
		return nil, status.Error(codes.InvalidArgument, "end must be after start")
	}

	// exclude self from overlap check
	if dup, err := h.store.HasOverlap(ctx, userID, start, end, req.Id); err != nil {
		return nil, status.Error(codes.Internal, "internal error")
	} else if dup {
		return nil, status.Error(codes.AlreadyExists, "time conflicts with existing appointment")
	}

	apt := &model.Appointment{
		ID:          req.Id,
		Title:       req.Title,
		Description: req.Description,
		StartTime:   start,
		EndTime:     end,
		UserID:      userID,
		Location:    req.Location,
		AttendeeIDs: req.AttendeeIds,
	}

	if err := h.store.UpdateAppointment(ctx, apt); err != nil {
		return nil, status.Error(codes.AlreadyExists, "time conflicts with existing appointment")
	}

	return &pb.UpdateAppointmentResponse{Appointment: toProto(apt)}, nil
}

func (h *Handler) DeleteAppointment(ctx context.Context, req *pb.DeleteAppointmentRequest) (*pb.DeleteAppointmentResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "id required")
	}

	if err := h.store.DeleteAppointment(ctx, req.Id, uid(ctx)); err != nil {
		return nil, status.Error(codes.Internal, "internal error")
	}
	return &pb.DeleteAppointmentResponse{}, nil
}

func toProto(a *model.Appointment) *pb.Appointment {
	p := &pb.Appointment{
		Id:          a.ID,
		Title:       a.Title,
		Description: a.Description,
		UserId:      a.UserID,
		Status:      a.Status,
		Location:    a.Location,
		AttendeeIds: a.AttendeeIDs,
	}
	if !a.StartTime.IsZero() {
		p.StartTime = timestamppb.New(a.StartTime)
	}
	if !a.EndTime.IsZero() {
		p.EndTime = timestamppb.New(a.EndTime)
	}
	if !a.CreatedAt.IsZero() {
		p.CreatedAt = timestamppb.New(a.CreatedAt)
	}
	if !a.UpdatedAt.IsZero() {
		p.UpdatedAt = timestamppb.New(a.UpdatedAt)
	}
	return p
}
