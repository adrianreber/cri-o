package private

import (
	"google.golang.org/grpc"

	"github.com/cri-o/cri-o/pkg/private"
	"github.com/cri-o/cri-o/server"
)

type Service interface {
	private.RuntimeServiceServer
}

type service struct {
	server *server.Server
}

// Register registers the runtime and image service with the provided grpc server
func Register(grpcServer *grpc.Server, crioServer *server.Server) {
	s := &service{crioServer}
	private.RegisterRuntimeServiceServer(grpcServer, s)
}
