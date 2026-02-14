package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"google.golang.org/grpc"

	pb "schedule-management-api/gen/appointment/v1"
	gweb "schedule-management-api/internal/grpcweb"
	"schedule-management-api/internal/handler"
	"schedule-management-api/internal/middleware"
	"schedule-management-api/internal/store"
)

func main() {
	_ = godotenv.Load()
	dbURL := env("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/scheduler?sslmode=disable")
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		log.Fatal("JWT_SECRET is required")
	}
	grpcPort := env("PORT", "50051")
	webPort := env("WEB_PORT", "8080")

	// database
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(context.Background()); err != nil {
		log.Fatalf("db ping: %v", err)
	}
	log.Println("connected to postgres")

	// run migrations
	if migration, err := os.ReadFile("db/migrations/001_init.sql"); err != nil {
		log.Printf("migration file not found, skipping: %v", err)
	} else if _, err := pool.Exec(context.Background(), string(migration)); err != nil {
		log.Printf("migration warning: %v", err)
	} else {
		log.Println("migration applied")
	}

	st := store.New(pool)
	h := handler.New(st, secret)

	// grpc server
	rl := middleware.NewRateLimiter(5, 10)
	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			middleware.RateLimit(rl),
			middleware.Auth(secret),
		),
	)
	pb.RegisterScheduleServiceServer(srv, h)

	// start grpc on TCP
	lis, err := net.Listen("tcp", ":"+grpcPort)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	go func() {
		log.Printf("grpc on :%s", grpcPort)
		if err := srv.Serve(lis); err != nil {
			log.Printf("grpc: %v", err)
		}
	}()

	// grpc-web bridge -> forwards browser requests to grpc on localhost
	bridge, err := gweb.New("localhost:"+grpcPort, h, secret)
	if err != nil {
		log.Fatalf("bridge: %v", err)
	}
	defer bridge.Close()

	httpSrv := &http.Server{
		Addr:    ":" + webPort,
		Handler: bridge.Handler(),
	}
	go func() {
		log.Printf("grpc-web on :%s", webPort)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("http: %v", err)
		}
	}()

	// graceful shutdown
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	log.Println("shutting down")
	srv.GracefulStop()
	httpSrv.Close()
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
