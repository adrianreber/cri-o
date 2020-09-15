package server

import (
	"fmt"

	"github.com/containers/podman/v3/libpod"
	"github.com/cri-o/cri-o/internal/lib"
	"github.com/cri-o/cri-o/pkg/private"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CheckpointContainer checkpoints a container
func (s *Server) CheckpointContainer(ctx context.Context, req *private.CheckpointContainerRequest) error {
	if !s.config.RuntimeConfig.CheckpointRestore() {
		return fmt.Errorf("checkpoint/restore support not available")
	}
	_, err := s.GetContainerFromShortID(req.Id)
	if err != nil {
		return status.Errorf(codes.NotFound, "could not find container %q: %v", req.Id, err)
	}

	opts := &lib.ContainerCheckpointRestoreOptions{
		Container: req.Id,
		ContainerCheckpointOptions: libpod.ContainerCheckpointOptions{
			TargetFile:  req.Options.CommonOptions.Archive,
			Keep:        req.Options.CommonOptions.Keep,
			KeepRunning: req.Options.LeaveRunning,
		},
	}

	_, err = s.ContainerServer.ContainerCheckpoint(ctx, opts)
	if err != nil {
		return err
	}

	return nil
}
