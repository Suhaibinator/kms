package grpcserver

import (
	"context"

	"google.golang.org/grpc"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"
)

type unaryHealthServer interface {
	Check(context.Context, *healthgrpc.HealthCheckRequest) (*healthgrpc.HealthCheckResponse, error)
	List(context.Context, *healthgrpc.HealthListRequest) (*healthgrpc.HealthListResponse, error)
}

// unaryHealthServiceDesc is an explicit allowlist of the standard health RPCs
// exposed by the KMS. New methods must be reviewed before they are registered.
var unaryHealthServiceDesc = grpc.ServiceDesc{
	ServiceName: "grpc.health.v1.Health",
	HandlerType: (*unaryHealthServer)(nil),
	Methods: []grpc.MethodDesc{
		{MethodName: "Check", Handler: healthCheckHandler},
		{MethodName: "List", Handler: healthListHandler},
	},
	Metadata: "grpc/health/v1/health.proto",
}

func registerUnaryHealthServer(registrar grpc.ServiceRegistrar, server unaryHealthServer) {
	registrar.RegisterService(&unaryHealthServiceDesc, server)
}

func healthCheckHandler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	req := new(healthgrpc.HealthCheckRequest)
	if err := dec(req); err != nil {
		return nil, err
	}
	server := srv.(unaryHealthServer)
	if interceptor == nil {
		return server.Check(ctx, req)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: healthgrpc.Health_Check_FullMethodName}
	handler := func(ctx context.Context, req any) (any, error) {
		return server.Check(ctx, req.(*healthgrpc.HealthCheckRequest))
	}
	return interceptor(ctx, req, info, handler)
}

func healthListHandler(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
	req := new(healthgrpc.HealthListRequest)
	if err := dec(req); err != nil {
		return nil, err
	}
	server := srv.(unaryHealthServer)
	if interceptor == nil {
		return server.List(ctx, req)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: healthgrpc.Health_List_FullMethodName}
	handler := func(ctx context.Context, req any) (any, error) {
		return server.List(ctx, req.(*healthgrpc.HealthListRequest))
	}
	return interceptor(ctx, req, info, handler)
}
