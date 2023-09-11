package server

import (
	"fmt"
	"os"
	"path/filepath"

	metadata "github.com/checkpoint-restore/checkpointctl/lib"
	"github.com/containers/podman/v4/libpod"
	"github.com/cri-o/cri-o/internal/log"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	types "k8s.io/cri-api/pkg/apis/runtime/v1"
)

// CheckpointContainer checkpoints a container
func (s *Server) CheckpointContainer(ctx context.Context, req *types.CheckpointContainerRequest) (*types.CheckpointContainerResponse, error) {
	if !s.config.RuntimeConfig.CheckpointRestore() {
		return nil, fmt.Errorf("checkpoint/restore support not available")
	}

	_, err := s.GetContainerFromShortID(ctx, req.ContainerId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "could not find container %q: %v", req.ContainerId, err)
	}

	if req.Location == "" {
		return nil, status.Errorf(
			codes.InvalidArgument,
			"checkpoint archive location needs to be set",
		)
	}

	fileInfo, err := os.Stat(filepath.Dir(req.Location))
	if err != nil {
		return nil, status.Errorf(
			codes.InvalidArgument,
			"could not stat %q: %v",
			filepath.Dir(req.Location),
			err,
		)
	}
	if !fileInfo.IsDir() {
		return nil, status.Errorf(
			codes.InvalidArgument,
			"%q is not a directory",
			filepath.Dir(req.Location),
		)
	}

	log.Infof(ctx, "Checkpointing container: %s", req.ContainerId)
	config := &metadata.ContainerConfig{
		ID: req.ContainerId,
	}
	opts := &libpod.ContainerCheckpointOptions{
		TargetFile: req.Location,
		// For the forensic container checkpointing use case we
		// keep the container running after checkpointing it.
		KeepRunning: true,
	}

	_, err = s.ContainerServer.ContainerCheckpoint(ctx, config, opts)
	if err != nil {
		return nil, err
	}

	log.Infof(ctx, "Checkpointed container: %s", req.ContainerId)

	return &types.CheckpointContainerResponse{}, nil
}
