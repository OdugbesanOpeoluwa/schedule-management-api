package middleware

import (
	"context"
	"strings"

	"schedule-management-api/internal/auth"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type ctxKey string

const UserIDKey ctxKey = "uid"

// skip auth for these
var open = map[string]bool{
	"/appointment.v1.ScheduleService/Register": true,
	"/appointment.v1.ScheduleService/Login":    true,
}

func Auth(secret string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		if open[info.FullMethod] {
			return next(ctx, req)
		}

		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing metadata")
		}

	// token from Authorization: Bearer <jwt>
		raw := ""
		vals := md.Get("authorization")
		if len(vals) > 0 {
			raw = strings.TrimPrefix(vals[0], "Bearer ")
		}

		if raw == "" {
			return nil, status.Error(codes.Unauthenticated, "no token")
		}

		claims, err := auth.ParseToken(raw, secret)
		if err != nil {
			return nil, status.Error(codes.Unauthenticated, "bad token")
		}

		ctx = context.WithValue(ctx, UserIDKey, claims.UserID)
		return next(ctx, req)
	}
}
