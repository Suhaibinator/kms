package grpcserver

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

func TestStandardHealthCheckRemainsPublic(t *testing.T) {
	env := newTestEnv(t, false)
	client := healthgrpc.NewHealthClient(env.conn)

	resp, err := client.Check(context.Background(), &healthgrpc.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("unauthenticated health check: %v", err)
	}
	if got := resp.GetStatus(); got != healthgrpc.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("health check status = %v, want NOT_SERVING", got)
	}
}

func TestStandardHealthServiceRegistersOnlyUnaryMethods(t *testing.T) {
	env := newTestEnv(t, true)
	service, ok := env.srv.GRPCServer().GetServiceInfo()["grpc.health.v1.Health"]
	if !ok {
		t.Fatal("standard health service is not registered")
	}

	methods := make(map[string]grpc.MethodInfo, len(service.Methods))
	for _, method := range service.Methods {
		methods[method.Name] = method
	}
	if len(methods) != 2 {
		t.Fatalf("registered health methods = %+v, want Check and List", service.Methods)
	}
	for _, name := range []string{"Check", "List"} {
		method, ok := methods[name]
		if !ok {
			t.Fatalf("registered health methods = %+v, missing %s", service.Methods, name)
		}
		if method.IsClientStream || method.IsServerStream {
			t.Fatalf("health method %s is streaming, want unary", name)
		}
	}
	if _, ok := methods["Watch"]; ok {
		t.Fatal("Health.Watch must not be registered")
	}
}

func TestStandardHealthListRemainsAuthenticated(t *testing.T) {
	env := newTestEnv(t, true)
	client := healthgrpc.NewHealthClient(env.conn)

	if _, err := client.List(context.Background(), &healthgrpc.HealthListRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated health list code = %v, want Unauthenticated (%v)", status.Code(err), err)
	}
	resp, err := client.List(adminCtx(), &healthgrpc.HealthListRequest{})
	if err != nil {
		t.Fatalf("authenticated health list: %v", err)
	}
	if got := resp.GetStatuses()[""].GetStatus(); got != healthgrpc.HealthCheckResponse_SERVING {
		t.Fatalf("overall health list status = %v, want SERVING", got)
	}
}

func TestStandardHealthWatchDisabled(t *testing.T) {
	env := newTestEnv(t, true)
	client := healthgrpc.NewHealthClient(env.conn)

	tests := []struct {
		name    string
		ctx     context.Context
		service string
		want    codes.Code
	}{
		{name: "unauthenticated", ctx: context.Background(), want: codes.Unimplemented},
		{name: "authenticated", ctx: adminCtx(), want: codes.Unimplemented},
		{name: "unknown service", ctx: adminCtx(), service: "attacker-selected", want: codes.Unimplemented},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stream, err := client.Watch(tc.ctx, &healthgrpc.HealthCheckRequest{Service: tc.service})
			if err == nil {
				_, err = stream.Recv()
			}
			if got := status.Code(err); got != tc.want {
				t.Fatalf("health watch code = %v, want %v (%v)", got, tc.want, err)
			}
		})
	}
}

func TestStandardHealthWatchRejectedBeforeRequestMessage(t *testing.T) {
	env := newTestEnv(t, true)
	ctx, cancel := context.WithTimeout(adminCtx(), time.Second)
	defer cancel()

	stream, err := env.conn.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, healthgrpc.Health_Watch_FullMethodName)
	if err != nil {
		t.Fatalf("open health watch: %v", err)
	}
	err = stream.RecvMsg(&healthgrpc.HealthCheckResponse{})
	if got := status.Code(err); got != codes.Unimplemented {
		t.Fatalf("health watch without request message = %v, want Unimplemented (%v)", got, err)
	}
}
