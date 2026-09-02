// Command parameter-store is the single binary for the parameter store and
// secret management service: the serve daemon plus administrative
// subcommands. It wires the gRPC server implementation into the CLI's
// integration seam and dispatches to internal/cli.
package main

import (
	"net"
	"os"

	"github.com/Suhaibinator/kms/internal/cli"
	"github.com/Suhaibinator/kms/internal/core"
	"github.com/Suhaibinator/kms/internal/server/grpcserver"
	"github.com/Suhaibinator/kms/internal/watch"
)

// grpcAdapter narrows *grpcserver.Server to cli.GRPCServer. The listener is
// bound at construction time so a bad address fails serve startup rather than
// surfacing later from the serve goroutine.
type grpcAdapter struct {
	srv *grpcserver.Server
	lis net.Listener
}

func (a *grpcAdapter) Serve() error  { return a.srv.Serve(a.lis) }
func (a *grpcAdapter) GracefulStop() { a.srv.GracefulStop() }
func (a *grpcAdapter) Stop()         { a.srv.Stop() }

func main() {
	cli.GRPCFactory = func(svc *core.Service, hub *watch.Hub, cfg cli.GRPCConfig) (cli.GRPCServer, error) {
		srv, err := grpcserver.New(svc, hub, grpcserver.Config{Addr: cfg.Addr, TLS: cfg.TLS, Metrics: cfg.Metrics})
		if err != nil {
			return nil, err
		}
		lis, err := srv.Listen()
		if err != nil {
			return nil, err
		}
		return &grpcAdapter{srv: srv, lis: lis}, nil
	}
	os.Exit(cli.New().Run(os.Args[1:]))
}
