package grpcweb

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "schedule-management-api/gen/appointment/v1"
	"schedule-management-api/internal/auth"
	"schedule-management-api/internal/handler"
	"schedule-management-api/internal/middleware"
)

// Bridge translates gRPC-Web (browser HTTP/1.1) → native gRPC via TCP.
type Bridge struct {
	conn   *grpc.ClientConn
	direct *handler.Handler
	secret string
}

// New dials the gRPC server at addr (e.g. "localhost:50051").
// If directHandler is provided, it bypasses network for specific methods.
func New(addr string, directHandler *handler.Handler, secret string) (*Bridge, error) {
	conn, err := grpc.NewClient(
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("grpcweb dial: %w", err)
	}
	return &Bridge{conn: conn, direct: directHandler, secret: secret}, nil
}

func (b *Bridge) Close() { b.conn.Close() }

// Handler returns an http.Handler that translates gRPC-Web → gRPC.
func (b *Bridge) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = "*"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers",
			"Content-Type, X-Grpc-Web, X-User-Agent, Authorization, x-grpc-web")
		w.Header().Set("Access-Control-Expose-Headers",
			"Grpc-Status, Grpc-Message, Grpc-Status-Details-Bin, grpc-status, grpc-message")
		w.Header().Set("Access-Control-Max-Age", "86400")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ct := r.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "application/grpc-web") {
			http.Error(w, "not grpc-web", http.StatusUnsupportedMediaType)
			return
		}

		log.Printf("grpc-web → %s", r.URL.Path)
		b.forward(w, r)
	})
}

func (b *Bridge) forward(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, codes.Internal, "read body failed")
		return
	}
	if len(body) < 5 {
		writeError(w, codes.InvalidArgument, "body too short")
		return
	}

	// grpc-web frame: 1-byte flag + 4-byte big-endian length + protobuf
	msgLen := binary.BigEndian.Uint32(body[1:5])
	if int(msgLen)+5 > len(body) {
		writeError(w, codes.InvalidArgument, "incomplete frame")
		return
	}
	payload := body[5 : 5+msgLen]

	// forward metadata
	md := metadata.MD{}
	if vals := r.Header.Values("Authorization"); len(vals) > 0 {
		md.Set("authorization", vals...)
	}
	ctx := metadata.NewOutgoingContext(r.Context(), md)

	// BYPASS: manually handle Login/Register if direct handler is available
	if b.direct != nil {
		if strings.HasSuffix(r.URL.Path, "/Login") {
			b.manualLogin(ctx, w, payload)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/Register") {
			b.manualRegister(ctx, w, payload)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/ListAppointments") {
			b.manualListAppointments(ctx, w, payload, r.Header.Get("Authorization"))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/CreateAppointment") {
			b.manualCreateAppointment(ctx, w, payload, r.Header.Get("Authorization"))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/GetAppointment") {
			b.manualGetAppointment(ctx, w, payload, r.Header.Get("Authorization"))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/UpdateAppointment") {
			b.manualUpdateAppointment(ctx, w, payload, r.Header.Get("Authorization"))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/DeleteAppointment") {
			b.manualDeleteAppointment(ctx, w, payload, r.Header.Get("Authorization"))
			return
		}
	}

	// invoke gRPC method using raw codec (pass-through bytes)
	resp := &rawMsg{}
	err = b.conn.Invoke(ctx, r.URL.Path, &rawMsg{data: payload}, resp, grpc.ForceCodec(rawCodec{}))
	if err != nil {
		st, _ := status.FromError(err)
		log.Printf("grpc-web error: %s: %s", st.Code(), st.Message())
		writeError(w, st.Code(), st.Message())
		return
	}

	writeSuccess(w, resp.data)
}

// rawMsg wraps raw protobuf bytes.
type rawMsg struct{ data []byte }

// rawCodec passes bytes through without marshal/unmarshal.
type rawCodec struct{}

func (rawCodec) Marshal(v any) ([]byte, error) {
	return v.(*rawMsg).data, nil
}
func (rawCodec) Unmarshal(data []byte, v any) error {
	m := v.(*rawMsg)
	m.data = append([]byte(nil), data...)
	return nil
}
func (rawCodec) Name() string { return "raw" }

func writeError(w http.ResponseWriter, code codes.Code, msg string) {
	w.Header().Set("Content-Type", "application/grpc-web+proto")
	w.WriteHeader(http.StatusOK)
	trailer := fmt.Sprintf("grpc-status:%d\r\ngrpc-message:%s\r\n", code, msg)
	tf := make([]byte, 5+len(trailer))
	tf[0] = 0x80
	binary.BigEndian.PutUint32(tf[1:5], uint32(len(trailer)))
	copy(tf[5:], trailer)
	w.Write(tf)
}

func writeSuccess(w http.ResponseWriter, data []byte) {
	w.Header().Set("Content-Type", "application/grpc-web+proto")
	w.WriteHeader(http.StatusOK)
	// data frame
	df := make([]byte, 5+len(data))
	df[0] = 0x00
	binary.BigEndian.PutUint32(df[1:5], uint32(len(data)))
	copy(df[5:], data)
	w.Write(df)
	// trailer frame
	trailer := "grpc-status:0\r\n"
	tf := make([]byte, 5+len(trailer))
	tf[0] = 0x80
	binary.BigEndian.PutUint32(tf[1:5], uint32(len(trailer)))
	copy(tf[5:], trailer)
	w.Write(tf)
}

// no-op context key to suppress lint
var _ context.Context

func (b *Bridge) manualAuth(ctx context.Context, authHeader string) (context.Context, error) {
	if authHeader == "" {
		return nil, status.Error(codes.Unauthenticated, "no token")
	}
	raw := strings.TrimPrefix(authHeader, "Bearer ")
	claims, err := auth.ParseToken(raw, b.secret)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "bad token")
	}
	return context.WithValue(ctx, middleware.UserIDKey, claims.UserID), nil
}

func (b *Bridge) manualLogin(ctx context.Context, w http.ResponseWriter, payload []byte) {
	req := &pb.LoginRequest{}
	// manual decode
	for len(payload) > 0 {
		num, typ, n := protowire.ConsumeTag(payload)
		if n < 0 {
			writeError(w, codes.InvalidArgument, "parse error")
			return
		}
		payload = payload[n:]
		if num == 1 && typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(payload)
			req.Email = string(v)
			payload = payload[n:]
		} else if num == 2 && typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(payload)
			req.Password = string(v)
			payload = payload[n:]
		} else {
			n := protowire.ConsumeFieldValue(num, typ, payload)
			if n < 0 {
				writeError(w, codes.InvalidArgument, "parse error")
				return
			}
			payload = payload[n:]
		}
	}

	resp, err := b.direct.Login(ctx, req)
	if err != nil {
		st, _ := status.FromError(err)
		writeError(w, st.Code(), st.Message())
		return
	}

	// manual encode response
	var out []byte
	out = protowire.AppendTag(out, 1, protowire.BytesType)
	out = protowire.AppendString(out, resp.Token)
	out = protowire.AppendTag(out, 2, protowire.BytesType)
	out = protowire.AppendString(out, resp.UserId)
	out = protowire.AppendTag(out, 3, protowire.BytesType)
	out = protowire.AppendString(out, resp.Name)

	writeSuccess(w, out)
}

func (b *Bridge) manualRegister(ctx context.Context, w http.ResponseWriter, payload []byte) {
	req := &pb.RegisterRequest{}
	for len(payload) > 0 {
		num, typ, n := protowire.ConsumeTag(payload)
		if n < 0 {
			writeError(w, codes.InvalidArgument, "parse error")
			return
		}
		payload = payload[n:]
		if num == 1 && typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(payload)
			req.Email = string(v)
			payload = payload[n:]
		} else if num == 2 && typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(payload)
			req.Password = string(v)
			payload = payload[n:]
		} else if num == 3 && typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(payload)
			req.Name = string(v)
			payload = payload[n:]
		} else {
			n := protowire.ConsumeFieldValue(num, typ, payload)
			if n < 0 {
				writeError(w, codes.InvalidArgument, "parse error")
				return
			}
			payload = payload[n:]
		}
	}

	resp, err := b.direct.Register(ctx, req)
	if err != nil {
		st, _ := status.FromError(err)
		writeError(w, st.Code(), st.Message())
		return
	}

	var out []byte
	out = protowire.AppendTag(out, 1, protowire.BytesType)
	out = protowire.AppendString(out, resp.UserId)
	out = protowire.AppendTag(out, 2, protowire.BytesType)
	out = protowire.AppendString(out, resp.Token)

	writeSuccess(w, out)
}

func (b *Bridge) manualListAppointments(ctx context.Context, w http.ResponseWriter, payload []byte, authHeader string) {
	ctx, err := b.manualAuth(ctx, authHeader)
	if err != nil {
		st, _ := status.FromError(err)
		writeError(w, st.Code(), st.Message())
		return
	}

	req := &pb.ListAppointmentsRequest{}
	for len(payload) > 0 {
		num, typ, n := protowire.ConsumeTag(payload)
		if n < 0 {
			writeError(w, codes.InvalidArgument, "parse error")
			return
		}
		payload = payload[n:]
		if num == 1 && typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(payload)
			req.RangeStart = parseTimestamp(v)
			payload = payload[n:]
		} else if num == 2 && typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(payload)
			req.RangeEnd = parseTimestamp(v)
			payload = payload[n:]
		} else {
			n := protowire.ConsumeFieldValue(num, typ, payload)
			if n < 0 {
				writeError(w, codes.InvalidArgument, "parse error")
				return
			}
			payload = payload[n:]
		}
	}

	resp, err := b.direct.ListAppointments(ctx, req)
	if err != nil {
		st, _ := status.FromError(err)
		writeError(w, st.Code(), st.Message())
		return
	}

	var out []byte
	for _, appt := range resp.Appointments {
		out = appendAppointment(out, 1, appt)
	}
	writeSuccess(w, out)
}

func parseTimestamp(b []byte) *timestamppb.Timestamp {
	ts := &timestamppb.Timestamp{}
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return ts
		}
		b = b[n:]
		if num == 1 && typ == protowire.VarintType {
			v, n := protowire.ConsumeVarint(b)
			ts.Seconds = int64(v)
			b = b[n:]
		} else if num == 2 && typ == protowire.VarintType {
			v, n := protowire.ConsumeVarint(b)
			ts.Nanos = int32(v)
			b = b[n:]
		} else {
			n := protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return ts
			}
			b = b[n:]
		}
	}
	return ts
}

func appendTimestamp(out []byte, num protowire.Number, ts *timestamppb.Timestamp) []byte {
	if ts == nil {
		return out
	}
	var inner []byte
	if ts.Seconds != 0 {
		inner = protowire.AppendTag(inner, 1, protowire.VarintType)
		inner = protowire.AppendVarint(inner, uint64(ts.Seconds))
	}
	if ts.Nanos != 0 {
		inner = protowire.AppendTag(inner, 2, protowire.VarintType)
		inner = protowire.AppendVarint(inner, uint64(ts.Nanos))
	}
	out = protowire.AppendTag(out, num, protowire.BytesType)
	out = protowire.AppendBytes(out, inner)
	return out
}

func appendAppointment(out []byte, num protowire.Number, a *pb.Appointment) []byte {
	if a == nil {
		return out
	}
	var inner []byte
	inner = protowire.AppendTag(inner, 1, protowire.BytesType)
	inner = protowire.AppendString(inner, a.Id)
	inner = protowire.AppendTag(inner, 2, protowire.BytesType)
	inner = protowire.AppendString(inner, a.Title)
	inner = protowire.AppendTag(inner, 3, protowire.BytesType)
	inner = protowire.AppendString(inner, a.Description)
	inner = appendTimestamp(inner, 4, a.StartTime)
	inner = appendTimestamp(inner, 5, a.EndTime)
	inner = protowire.AppendTag(inner, 6, protowire.BytesType)
	inner = protowire.AppendString(inner, a.UserId)
	inner = protowire.AppendTag(inner, 7, protowire.BytesType)
	inner = protowire.AppendString(inner, a.Status)
	inner = protowire.AppendTag(inner, 8, protowire.BytesType)
	inner = protowire.AppendString(inner, a.Location)
	for _, att := range a.AttendeeIds {
		inner = protowire.AppendTag(inner, 9, protowire.BytesType)
		inner = protowire.AppendString(inner, att)
	}
	inner = appendTimestamp(inner, 10, a.CreatedAt)
	inner = appendTimestamp(inner, 11, a.UpdatedAt)

	out = protowire.AppendTag(out, num, protowire.BytesType)
	out = protowire.AppendBytes(out, inner)
	return out
}

func (b *Bridge) manualCreateAppointment(ctx context.Context, w http.ResponseWriter, payload []byte, authHeader string) {
	ctx, err := b.manualAuth(ctx, authHeader)
	if err != nil {
		st, _ := status.FromError(err)
		writeError(w, st.Code(), st.Message())
		return
	}

	req := &pb.CreateAppointmentRequest{}
	for len(payload) > 0 {
		num, typ, n := protowire.ConsumeTag(payload)
		if n < 0 {
			writeError(w, codes.InvalidArgument, "parse error")
			return
		}
		payload = payload[n:]
		if num == 1 && typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(payload)
			req.Title = string(v)
			payload = payload[n:]
		} else if num == 2 && typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(payload)
			req.Description = string(v)
			payload = payload[n:]
		} else if num == 3 && typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(payload)
			req.StartTime = parseTimestamp(v)
			payload = payload[n:]
		} else if num == 4 && typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(payload)
			req.EndTime = parseTimestamp(v)
			payload = payload[n:]
		} else if num == 5 && typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(payload)
			req.Location = string(v)
			payload = payload[n:]
		} else if num == 6 && typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(payload)
			// According to proto spec, repeated fields can be packed or not.
			// protowire handles simple repeated bytes as sequential fields.
			req.AttendeeIds = append(req.AttendeeIds, string(v))
			payload = payload[n:]
		} else {
			n := protowire.ConsumeFieldValue(num, typ, payload)
			if n < 0 {
				writeError(w, codes.InvalidArgument, "parse error")
				return
			}
			payload = payload[n:]
		}
	}

	resp, err := b.direct.CreateAppointment(ctx, req)
	if err != nil {
		st, _ := status.FromError(err)
		writeError(w, st.Code(), st.Message())
		return
	}

	var out []byte
	out = appendAppointment(out, 1, resp.Appointment)
	writeSuccess(w, out)
}

func (b *Bridge) manualGetAppointment(ctx context.Context, w http.ResponseWriter, payload []byte, authHeader string) {
	ctx, err := b.manualAuth(ctx, authHeader)
	if err != nil {
		st, _ := status.FromError(err)
		writeError(w, st.Code(), st.Message())
		return
	}

	req := &pb.GetAppointmentRequest{}
	for len(payload) > 0 {
		num, typ, n := protowire.ConsumeTag(payload)
		if n < 0 {
			writeError(w, codes.InvalidArgument, "parse error")
			return
		}
		payload = payload[n:]
		if num == 1 && typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(payload)
			req.Id = string(v)
			payload = payload[n:]
		} else {
			n := protowire.ConsumeFieldValue(num, typ, payload)
			payload = payload[n:]
		}
	}

	resp, err := b.direct.GetAppointment(ctx, req)
	if err != nil {
		st, _ := status.FromError(err)
		writeError(w, st.Code(), st.Message())
		return
	}

	var out []byte
	out = appendAppointment(out, 1, resp.Appointment)
	writeSuccess(w, out)
}

func (b *Bridge) manualUpdateAppointment(ctx context.Context, w http.ResponseWriter, payload []byte, authHeader string) {
	ctx, err := b.manualAuth(ctx, authHeader)
	if err != nil {
		st, _ := status.FromError(err)
		writeError(w, st.Code(), st.Message())
		return
	}

	req := &pb.UpdateAppointmentRequest{}
	for len(payload) > 0 {
		num, typ, n := protowire.ConsumeTag(payload)
		if n < 0 {
			writeError(w, codes.InvalidArgument, "parse error")
			return
		}
		payload = payload[n:]
		if num == 1 && typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(payload)
			req.Id = string(v)
			payload = payload[n:]
		} else if num == 2 && typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(payload)
			req.Title = string(v)
			payload = payload[n:]
		} else if num == 3 && typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(payload)
			req.Description = string(v)
			payload = payload[n:]
		} else if num == 4 && typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(payload)
			req.StartTime = parseTimestamp(v)
			payload = payload[n:]
		} else if num == 5 && typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(payload)
			req.EndTime = parseTimestamp(v)
			payload = payload[n:]
		} else if num == 6 && typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(payload)
			req.Location = string(v)
			payload = payload[n:]
		} else if num == 7 && typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(payload)
			req.AttendeeIds = append(req.AttendeeIds, string(v))
			payload = payload[n:]
		} else {
			n := protowire.ConsumeFieldValue(num, typ, payload)
			payload = payload[n:]
		}
	}

	resp, err := b.direct.UpdateAppointment(ctx, req)
	if err != nil {
		st, _ := status.FromError(err)
		writeError(w, st.Code(), st.Message())
		return
	}

	var out []byte
	out = appendAppointment(out, 1, resp.Appointment)
	writeSuccess(w, out)
}

func (b *Bridge) manualDeleteAppointment(ctx context.Context, w http.ResponseWriter, payload []byte, authHeader string) {
	ctx, err := b.manualAuth(ctx, authHeader)
	if err != nil {
		st, _ := status.FromError(err)
		writeError(w, st.Code(), st.Message())
		return
	}

	req := &pb.DeleteAppointmentRequest{}
	for len(payload) > 0 {
		num, typ, n := protowire.ConsumeTag(payload)
		if n < 0 {
			writeError(w, codes.InvalidArgument, "parse error")
			return
		}
		payload = payload[n:]
		if num == 1 && typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(payload)
			req.Id = string(v)
			payload = payload[n:]
		} else {
			n := protowire.ConsumeFieldValue(num, typ, payload)
			payload = payload[n:]
		}
	}

	_, err = b.direct.DeleteAppointment(ctx, req)
	if err != nil {
		st, _ := status.FromError(err)
		writeError(w, st.Code(), st.Message())
		return
	}

	writeSuccess(w, nil)
}

