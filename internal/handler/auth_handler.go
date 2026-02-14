package handler

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"schedule-management-api/internal/auth"
	"schedule-management-api/internal/model"
	pb "schedule-management-api/gen/appointment/v1"
)

func (h *Handler) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	if req.Email == "" || req.Password == "" || req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "all fields required")
	}
	if len(req.Password) < 8 {
		return nil, status.Error(codes.InvalidArgument, "password too short")
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		return nil, status.Error(codes.Internal, "internal error")
	}

	u := &model.User{
		ID:           uuid.New().String(),
		Email:        req.Email,
		PasswordHash: hash,
		Name:         req.Name,
	}

	if err := h.store.CreateUser(ctx, u); err != nil {
		// unique violation = dup email, but don't reveal that
		return nil, status.Error(codes.AlreadyExists, "registration failed")
	}

	tok, err := auth.MakeToken(u.ID, h.secret)
	if err != nil {
		return nil, status.Error(codes.Internal, "internal error")
	}

	return &pb.RegisterResponse{UserId: u.ID, Token: tok}, nil
}

func (h *Handler) Login(ctx context.Context, req *pb.LoginRequest) (*pb.LoginResponse, error) {
	if req.Email == "" || req.Password == "" {
		return nil, status.Error(codes.InvalidArgument, "email and password required")
	}

	u, err := h.store.UserByEmail(ctx, req.Email)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid credentials")
	}

	if !auth.CheckPassword(u.PasswordHash, req.Password) {
		return nil, status.Error(codes.Unauthenticated, "invalid credentials")
	}

	tok, err := auth.MakeToken(u.ID, h.secret)
	if err != nil {
		return nil, status.Error(codes.Internal, "internal error")
	}

	return &pb.LoginResponse{Token: tok, UserId: u.ID, Name: u.Name}, nil
}
