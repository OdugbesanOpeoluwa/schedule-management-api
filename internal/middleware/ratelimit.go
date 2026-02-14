package middleware

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"context"
)

type client struct {
	lim  *rate.Limiter
	seen time.Time
}

type RateLimiter struct {
	mu      sync.Mutex
	clients map[string]*client
	r       rate.Limit
	burst   int
}

func NewRateLimiter(rps float64, burst int) *RateLimiter {
	rl := &RateLimiter{
		clients: make(map[string]*client),
		r:       rate.Limit(rps),
		burst:   burst,
	}
	// cleanup stale entries every minute
	go func() {
		for {
			time.Sleep(time.Minute)
			rl.mu.Lock()
			for ip, c := range rl.clients {
				if time.Since(c.seen) > 3*time.Minute {
					delete(rl.clients, ip)
				}
			}
			rl.mu.Unlock()
		}
	}()
	return rl
}

func (rl *RateLimiter) get(ip string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if c, ok := rl.clients[ip]; ok {
		c.seen = time.Now()
		return c.lim
	}
	l := rate.NewLimiter(rl.r, rl.burst)
	rl.clients[ip] = &client{lim: l, seen: time.Now()}
	return l
}

// methods that should be rate limited
var limited = map[string]bool{
	"/appointment.v1.ScheduleService/Register": true,
	"/appointment.v1.ScheduleService/Login":    true,
}

func RateLimit(rl *RateLimiter) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		if !limited[info.FullMethod] {
			return next(ctx, req)
		}
		ip := "unknown"
		if p, ok := peer.FromContext(ctx); ok {
			ip = p.Addr.String()
		}
		if !rl.get(ip).Allow() {
			return nil, status.Error(codes.ResourceExhausted, "too many requests")
		}
		return next(ctx, req)
	}
}
