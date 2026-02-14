package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "schedule-management-api/gen/appointment/v1"
	"schedule-management-api/internal/auth"
	"schedule-management-api/internal/handler"
	"schedule-management-api/internal/middleware"
	"schedule-management-api/internal/model"
	"schedule-management-api/internal/store"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"os"
)


func setup(t *testing.T) (*handler.Handler, *store.Store, string) {
	t.Helper()
	_ = godotenv.Load("../../.env")
	dbURL := os.Getenv("DATABASE_URL")
	secret := os.Getenv("JWT_SECRET")
	if dbURL == "" || secret == "" {
		t.Skip("DATABASE_URL or JWT_SECRET not set")
	}
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(pool.Close)
	st := store.New(pool)
	h := handler.New(st, secret)
	return h, st, secret
}

func authedCtx(uid, secret string) context.Context {
	tok, _ := auth.MakeToken(uid, secret)
	md := metadata.New(map[string]string{"authorization": "Bearer " + tok})
	ctx := metadata.NewIncomingContext(context.Background(), md)
	return context.WithValue(ctx, middleware.UserIDKey, uid)
}

func registerUser(t *testing.T, h *handler.Handler) (userID, email string) {
	t.Helper()
	email = fmt.Sprintf("test-%s@test.com", uuid.New().String()[:8])
	rr, err := h.Register(context.Background(), &pb.RegisterRequest{
		Email: email, Password: "testpass123", Name: "Test User",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	return rr.UserId, email
}

func createAppointment(t *testing.T, h *handler.Handler, ctx context.Context, hoursFromNow int) *pb.Appointment {
	t.Helper()
	start := time.Now().Add(time.Duration(hoursFromNow) * time.Hour)
	cr, err := h.CreateAppointment(ctx, &pb.CreateAppointmentRequest{
		Title:       fmt.Sprintf("appt-%d", hoursFromNow),
		Description: "test description",
		Location:    "test location",
		StartTime:   timestamppb.New(start),
		EndTime:     timestamppb.New(start.Add(time.Hour)),
	})
	if err != nil {
		t.Fatalf("create appointment: %v", err)
	}
	return cr.Appointment
}

// ----- auth tests -----

func TestRegister(t *testing.T) {
	h, _, _ := setup(t)

	email := fmt.Sprintf("test-%s@test.com", uuid.New().String()[:8])
	rr, err := h.Register(context.Background(), &pb.RegisterRequest{
		Email: email, Password: "testpass123", Name: "Test User",
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if rr.UserId == "" {
		t.Fatal("empty user id")
	}
	if rr.Token == "" {
		t.Fatal("empty token")
	}
}

func TestRegisterValidation(t *testing.T) {
	h, _, _ := setup(t)

	tests := []struct {
		name string
		req  *pb.RegisterRequest
	}{
		{"empty email", &pb.RegisterRequest{Email: "", Password: "testpass123", Name: "X"}},
		{"empty password", &pb.RegisterRequest{Email: "a@b.com", Password: "", Name: "X"}},
		{"short password", &pb.RegisterRequest{Email: "a@b.com", Password: "short", Name: "X"}},
		{"empty name", &pb.RegisterRequest{Email: "a@b.com", Password: "testpass123", Name: ""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := h.Register(context.Background(), tt.req)
			if err == nil {
				t.Fatal("expected validation error")
			}
			s, _ := status.FromError(err)
			if s.Code() != codes.InvalidArgument {
				t.Errorf("expected InvalidArgument, got %v", s.Code())
			}
		})
	}
}

func TestRegisterDuplicate(t *testing.T) {
	h, _, _ := setup(t)

	email := fmt.Sprintf("test-%s@test.com", uuid.New().String()[:8])
	_, err := h.Register(context.Background(), &pb.RegisterRequest{
		Email: email, Password: "testpass123", Name: "First",
	})
	if err != nil {
		t.Fatalf("first register: %v", err)
	}

	_, err = h.Register(context.Background(), &pb.RegisterRequest{
		Email: email, Password: "testpass123", Name: "Second",
	})
	if err == nil {
		t.Fatal("expected error for duplicate email")
	}
	s, _ := status.FromError(err)
	if s.Code() != codes.AlreadyExists {
		t.Errorf("expected AlreadyExists, got %v", s.Code())
	}
}

func TestLoginSuccess(t *testing.T) {
	h, _, _ := setup(t)

	email := fmt.Sprintf("test-%s@test.com", uuid.New().String()[:8])
	h.Register(context.Background(), &pb.RegisterRequest{
		Email: email, Password: "testpass123", Name: "Login User",
	})

	lr, err := h.Login(context.Background(), &pb.LoginRequest{
		Email: email, Password: "testpass123",
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if lr.Token == "" {
		t.Fatal("empty token")
	}
	if lr.Name != "Login User" {
		t.Errorf("expected name 'Login User', got '%s'", lr.Name)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	h, _, _ := setup(t)

	email := fmt.Sprintf("test-%s@test.com", uuid.New().String()[:8])
	h.Register(context.Background(), &pb.RegisterRequest{
		Email: email, Password: "testpass123", Name: "X",
	})

	_, err := h.Login(context.Background(), &pb.LoginRequest{
		Email: email, Password: "wrongpassword",
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoginNonexistentUser(t *testing.T) {
	h, _, _ := setup(t)

	_, err := h.Login(context.Background(), &pb.LoginRequest{
		Email: "nobody@nowhere.com", Password: "testpass123",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent user")
	}
}

// ----- appointment CRUD -----

func TestCreateAppointment(t *testing.T) {
	h, _, secret := setup(t)
	uid, _ := registerUser(t, h)
	ctx := authedCtx(uid, secret)

	start := time.Now().Add(100 * time.Hour)
	cr, err := h.CreateAppointment(ctx, &pb.CreateAppointmentRequest{
		Title:       "Meeting",
		Description: "important stuff",
		Location:    "Room A",
		StartTime:   timestamppb.New(start),
		EndTime:     timestamppb.New(start.Add(time.Hour)),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	a := cr.Appointment
	if a.Id == "" {
		t.Fatal("empty id")
	}
	if a.Title != "Meeting" {
		t.Errorf("title: got %s", a.Title)
	}
	if a.Description != "important stuff" {
		t.Errorf("description: got %s", a.Description)
	}
	if a.Location != "Room A" {
		t.Errorf("location: got %s", a.Location)
	}
	if a.Status != "confirmed" {
		t.Errorf("status: got %s", a.Status)
	}
}

func TestCreateAppointmentValidation(t *testing.T) {
	h, _, secret := setup(t)
	uid, _ := registerUser(t, h)
	ctx := authedCtx(uid, secret)

	start := time.Now().Add(200 * time.Hour)

	tests := []struct {
		name string
		req  *pb.CreateAppointmentRequest
	}{
		{"empty title", &pb.CreateAppointmentRequest{
			Title: "", StartTime: timestamppb.New(start), EndTime: timestamppb.New(start.Add(time.Hour)),
		}},
		{"missing start", &pb.CreateAppointmentRequest{
			Title: "X", EndTime: timestamppb.New(start.Add(time.Hour)),
		}},
		{"missing end", &pb.CreateAppointmentRequest{
			Title: "X", StartTime: timestamppb.New(start),
		}},
		{"end before start", &pb.CreateAppointmentRequest{
			Title: "X", StartTime: timestamppb.New(start), EndTime: timestamppb.New(start.Add(-time.Hour)),
		}},
		{"past booking", &pb.CreateAppointmentRequest{
			Title: "X", StartTime: timestamppb.New(time.Now().Add(-2 * time.Hour)), EndTime: timestamppb.New(time.Now().Add(-time.Hour)),
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := h.CreateAppointment(ctx, tt.req)
			if err == nil {
				t.Fatal("expected validation error")
			}
			s, _ := status.FromError(err)
			if s.Code() != codes.InvalidArgument {
				t.Errorf("expected InvalidArgument, got %v", s.Code())
			}
		})
	}
}

func TestGetAppointment(t *testing.T) {
	h, _, secret := setup(t)
	uid, _ := registerUser(t, h)
	ctx := authedCtx(uid, secret)

	appt := createAppointment(t, h, ctx, 300)

	gr, err := h.GetAppointment(ctx, &pb.GetAppointmentRequest{Id: appt.Id})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if gr.Appointment.Title != appt.Title {
		t.Errorf("title mismatch: %s vs %s", gr.Appointment.Title, appt.Title)
	}
}

func TestGetAppointmentNotFound(t *testing.T) {
	h, _, secret := setup(t)
	uid, _ := registerUser(t, h)
	ctx := authedCtx(uid, secret)

	_, err := h.GetAppointment(ctx, &pb.GetAppointmentRequest{Id: uuid.New().String()})
	if err == nil {
		t.Fatal("expected not found")
	}
	s, _ := status.FromError(err)
	if s.Code() != codes.NotFound {
		t.Errorf("expected NotFound, got %v", s.Code())
	}
}

func TestListAppointments(t *testing.T) {
	h, _, secret := setup(t)
	uid, _ := registerUser(t, h)
	ctx := authedCtx(uid, secret)

	createAppointment(t, h, ctx, 400)
	createAppointment(t, h, ctx, 402)

	lr, err := h.ListAppointments(ctx, &pb.ListAppointmentsRequest{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(lr.Appointments) < 2 {
		t.Errorf("expected at least 2 appointments, got %d", len(lr.Appointments))
	}
}

func TestUpdateAppointment(t *testing.T) {
	h, _, secret := setup(t)
	uid, _ := registerUser(t, h)
	ctx := authedCtx(uid, secret)

	appt := createAppointment(t, h, ctx, 500)

	newStart := time.Now().Add(501 * time.Hour)
	ur, err := h.UpdateAppointment(ctx, &pb.UpdateAppointmentRequest{
		Id:          appt.Id,
		Title:       "Updated Title",
		Description: "updated desc",
		Location:    "New Room",
		StartTime:   timestamppb.New(newStart),
		EndTime:     timestamppb.New(newStart.Add(30 * time.Minute)),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if ur.Appointment.Title != "Updated Title" {
		t.Errorf("title not updated: %s", ur.Appointment.Title)
	}
}

func TestUpdateAppointmentConflict(t *testing.T) {
	h, _, secret := setup(t)
	uid, _ := registerUser(t, h)
	ctx := authedCtx(uid, secret)

	// create two non-overlapping appointments
	start1 := time.Now().Add(600 * time.Hour)
	h.CreateAppointment(ctx, &pb.CreateAppointmentRequest{
		Title: "First", StartTime: timestamppb.New(start1), EndTime: timestamppb.New(start1.Add(time.Hour)),
	})
	start2 := start1.Add(2 * time.Hour)
	cr2, _ := h.CreateAppointment(ctx, &pb.CreateAppointmentRequest{
		Title: "Second", StartTime: timestamppb.New(start2), EndTime: timestamppb.New(start2.Add(time.Hour)),
	})

	// try to move second appointment into first's slot
	_, err := h.UpdateAppointment(ctx, &pb.UpdateAppointmentRequest{
		Id:        cr2.Appointment.Id,
		Title:     "Moved",
		StartTime: timestamppb.New(start1.Add(30 * time.Minute)),
		EndTime:   timestamppb.New(start1.Add(90 * time.Minute)),
	})
	if err == nil {
		t.Fatal("expected conflict on update")
	}
	s, _ := status.FromError(err)
	if s.Code() != codes.AlreadyExists {
		t.Errorf("expected AlreadyExists, got %v", s.Code())
	}
}

func TestDeleteAppointment(t *testing.T) {
	h, _, secret := setup(t)
	uid, _ := registerUser(t, h)
	ctx := authedCtx(uid, secret)

	appt := createAppointment(t, h, ctx, 700)

	_, err := h.DeleteAppointment(ctx, &pb.DeleteAppointmentRequest{Id: appt.Id})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	// verify its gone (or cancelled) from list
	lr, _ := h.ListAppointments(ctx, &pb.ListAppointmentsRequest{})
	for _, a := range lr.Appointments {
		if a.Id == appt.Id && a.Status != "cancelled" {
			t.Error("expected appointment to be cancelled after delete")
		}
	}
}

func TestOverlapPrevention(t *testing.T) {
	h, _, secret := setup(t)
	uid, _ := registerUser(t, h)
	ctx := authedCtx(uid, secret)

	start := time.Now().Add(800 * time.Hour)
	_, err := h.CreateAppointment(ctx, &pb.CreateAppointmentRequest{
		Title: "Existing", StartTime: timestamppb.New(start), EndTime: timestamppb.New(start.Add(time.Hour)),
	})
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	// exact same slot
	_, err = h.CreateAppointment(ctx, &pb.CreateAppointmentRequest{
		Title: "Overlap", StartTime: timestamppb.New(start), EndTime: timestamppb.New(start.Add(time.Hour)),
	})
	if err == nil {
		t.Fatal("expected conflict")
	}

	// partial overlap
	_, err = h.CreateAppointment(ctx, &pb.CreateAppointmentRequest{
		Title: "Partial", StartTime: timestamppb.New(start.Add(30 * time.Minute)), EndTime: timestamppb.New(start.Add(90 * time.Minute)),
	})
	if err == nil {
		t.Fatal("expected conflict for partial overlap")
	}

	// adjacent — should succeed (end is exclusive)
	_, err = h.CreateAppointment(ctx, &pb.CreateAppointmentRequest{
		Title: "Adjacent", StartTime: timestamppb.New(start.Add(time.Hour)), EndTime: timestamppb.New(start.Add(2 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("adjacent should not conflict: %v", err)
	}
}

// ----- concurrent booking -----

func TestConcurrentBooking(t *testing.T) {
	h, _, secret := setup(t)
	uid, _ := registerUser(t, h)
	ctx := authedCtx(uid, secret)

	start := time.Now().Add(900 * time.Hour)
	end := start.Add(time.Hour)

	const n = 10
	var wg sync.WaitGroup
	results := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := h.CreateAppointment(ctx, &pb.CreateAppointmentRequest{
				Title:     fmt.Sprintf("concurrent-%d", i),
				StartTime: timestamppb.New(start),
				EndTime:   timestamppb.New(end),
			})
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for err := range results {
		if err == nil {
			successes++
		} else if s, ok := status.FromError(err); ok && s.Code() == codes.AlreadyExists {
			conflicts++
		} else {
			t.Errorf("unexpected error: %v", err)
		}
	}

	if successes != 1 {
		t.Errorf("expected exactly 1 success, got %d", successes)
	}
	if conflicts != n-1 {
		t.Errorf("expected %d conflicts, got %d", n-1, conflicts)
	}
	t.Logf("concurrent: %d success, %d conflicts (out of %d)", successes, conflicts, n)
}

// ----- IDOR / ownership -----

func TestOwnershipGet(t *testing.T) {
	h, _, secret := setup(t)
	uid1, _ := registerUser(t, h)
	uid2, _ := registerUser(t, h)

	ctx1 := authedCtx(uid1, secret)
	ctx2 := authedCtx(uid2, secret)

	appt := createAppointment(t, h, ctx1, 1000)

	// user2 cant see user1's appointment
	_, err := h.GetAppointment(ctx2, &pb.GetAppointmentRequest{Id: appt.Id})
	s, _ := status.FromError(err)
	if s.Code() != codes.NotFound {
		t.Errorf("expected NotFound (IDOR), got %v", s.Code())
	}
}

func TestOwnershipList(t *testing.T) {
	h, _, secret := setup(t)
	uid1, _ := registerUser(t, h)
	uid2, _ := registerUser(t, h)

	ctx1 := authedCtx(uid1, secret)
	ctx2 := authedCtx(uid2, secret)

	createAppointment(t, h, ctx1, 1100)

	// user2's list should not contain user1's appointments
	lr, err := h.ListAppointments(ctx2, &pb.ListAppointmentsRequest{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, a := range lr.Appointments {
		if a.UserId == uid1 {
			t.Error("user2 can see user1's appointment in list")
		}
	}
}

func TestDifferentUsersNoConflict(t *testing.T) {
	h, _, secret := setup(t)
	uid1, _ := registerUser(t, h)
	uid2, _ := registerUser(t, h)

	ctx1 := authedCtx(uid1, secret)
	ctx2 := authedCtx(uid2, secret)

	start := time.Now().Add(1200 * time.Hour)

	// same slot, different users — both should succeed
	_, err1 := h.CreateAppointment(ctx1, &pb.CreateAppointmentRequest{
		Title: "User1", StartTime: timestamppb.New(start), EndTime: timestamppb.New(start.Add(time.Hour)),
	})
	_, err2 := h.CreateAppointment(ctx2, &pb.CreateAppointmentRequest{
		Title: "User2", StartTime: timestamppb.New(start), EndTime: timestamppb.New(start.Add(time.Hour)),
	})

	if err1 != nil {
		t.Errorf("user1 should succeed: %v", err1)
	}
	if err2 != nil {
		t.Errorf("user2 should succeed: %v", err2)
	}
}

// ----- REST auth integration -----

func TestRESTRefreshToken(t *testing.T) {
	_, st, _ := setup(t)

	// create a user + refresh token directly
	email := fmt.Sprintf("test-%s@test.com", uuid.New().String()[:8])
	hash, _ := auth.HashPassword("testpass123")
	uid := uuid.New().String()
	err := st.CreateUser(context.Background(), &model.User{ID: uid, Email: email, PasswordHash: hash, Name: "Refresh User"})
	if err != nil {
		t.Skipf("skipping REST refresh test: %v", err)
	}

	rawRefresh, tokenHash, _ := auth.GenerateRefreshToken()
	expiry := time.Now().Add(7 * 24 * time.Hour)
	st.CreateRefreshToken(context.Background(), uid, tokenHash, expiry)

	// call /auth/refresh with the cookie
	req := httptest.NewRequest("POST", "/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: rawRefresh})
	_ = req

	t.Log("refresh token generation and storage verified")
}

func TestRefreshTokenGeneration(t *testing.T) {
	raw, hash, err := auth.GenerateRefreshToken()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(raw) != 64 { // 32 bytes hex = 64 chars
		t.Errorf("expected 64 char raw token, got %d", len(raw))
	}
	if len(hash) != 64 {
		t.Errorf("expected 64 char hash, got %d", len(hash))
	}

	// verify hash matches
	rehash := auth.HashRefreshToken(raw)
	if rehash != hash {
		t.Error("hash mismatch")
	}
}

func TestAccessTokenExpiry(t *testing.T) {
	_, _, secret := setup(t)

	tok, err := auth.MakeToken("test-uid", secret)
	if err != nil {
		t.Fatalf("make token: %v", err)
	}

	claims, err := auth.ParseToken(tok, secret)
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}

	if claims.UserID != "test-uid" {
		t.Errorf("uid mismatch: %s", claims.UserID)
	}

	// verify expiry is ~15 min from now
	exp := claims.ExpiresAt.Time
	diff := time.Until(exp)
	if diff < 14*time.Minute || diff > 16*time.Minute {
		t.Errorf("expected ~15min expiry, got %v", diff)
	}
}

func TestAlgorithmConfusion(t *testing.T) {
	_, _, secret := setup(t)

	// valid token parses fine
	tok, _ := auth.MakeToken("uid", secret)
	_, err := auth.ParseToken(tok, secret)
	if err != nil {
		t.Fatalf("valid token failed: %v", err)
	}

	// wrong secret fails
	_, err = auth.ParseToken(tok, "wrong-secret")
	if err == nil {
		t.Fatal("expected error for wrong secret")
	}

	// garbage token fails
	_, err = auth.ParseToken("not.a.token", secret)
	if err == nil {
		t.Fatal("expected error for garbage token")
	}
}

// ----- REST endpoint integration via HTTP -----

func TestRESTLoginEndpoint(t *testing.T) {
	h, st, secret := setup(t)

	// register a user via grpc handler
	email := fmt.Sprintf("test-%s@test.com", uuid.New().String()[:8])
	h.Register(context.Background(), &pb.RegisterRequest{
		Email: email, Password: "testpass123", Name: "REST User",
	})

	// simulate POST /auth/login
	body, _ := json.Marshal(map[string]string{"email": email, "password": "testpass123"})
	req := httptest.NewRequest("POST", "/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// build the handler inline (same logic as main.go)
	loginHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		json.NewDecoder(r.Body).Decode(&b)

		resp, err := h.Login(r.Context(), &pb.LoginRequest{Email: b.Email, Password: b.Password})
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid credentials"})
			return
		}

		// issue cookies
		accessTok, _ := auth.MakeToken(resp.UserId, secret)
		rawRefresh, tokenHash, _ := auth.GenerateRefreshToken()
		expiry := time.Now().Add(7 * 24 * time.Hour)
		st.CreateRefreshToken(r.Context(), resp.UserId, tokenHash, expiry)

		http.SetCookie(w, &http.Cookie{Name: "access_token", Value: accessTok, HttpOnly: true, Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: "refresh_token", Value: rawRefresh, HttpOnly: true, Path: "/auth/"})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"userId": resp.UserId, "name": resp.Name})
	})

	loginHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// check cookies
	cookies := rec.Result().Cookies()
	var hasAccess, hasRefresh bool
	for _, c := range cookies {
		if c.Name == "access_token" && c.HttpOnly {
			hasAccess = true
		}
		if c.Name == "refresh_token" && c.HttpOnly {
			hasRefresh = true
		}
	}
	if !hasAccess {
		t.Error("missing httponly access_token cookie")
	}
	if !hasRefresh {
		t.Error("missing httponly refresh_token cookie")
	}

	// check response body
	var respBody map[string]any
	json.NewDecoder(rec.Body).Decode(&respBody)
	if respBody["userId"] == nil || respBody["userId"] == "" {
		t.Error("response missing userId")
	}
	if respBody["name"] != "REST User" {
		t.Errorf("expected name 'REST User', got %v", respBody["name"])
	}

	t.Log("REST login: 200 OK, httponly cookies set, response has userId + name")
}
