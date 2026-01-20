package server

import (
	"context"
	"errors"

	metadata "github.com/checkpoint-restore/checkpointctl/lib"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	types "k8s.io/cri-api/pkg/apis/runtime/v1"

	"github.com/cri-o/cri-o/internal/lib"
	"github.com/cri-o/cri-o/internal/log"
)

// CheckpointPod checkpoints a pod sandbox.
func (s *Server) CheckpointPod(ctx context.Context, req *types.CheckpointPodRequest) (*types.CheckpointPodResponse, error) {
	if !s.config.CheckpointRestore() {
		return nil, errors.New("checkpoint/restore support not available")
	}

	sb, err := s.getPodSandboxFromRequest(ctx, req.GetPodSandboxId())
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "could not find pod sandbox %q: %v", req.GetPodSandboxId(), err)
	}

	// TargetFile is required for pod checkpoints
	if req.GetLocation() == "" {
		return nil, status.Error(codes.InvalidArgument, "location (target file) is required for pod checkpoint")
	}

	log.Infof(ctx, "Checkpointing pod sandbox: %s", req.GetPodSandboxId())
	config := &metadata.ContainerConfig{
		ID: sb.ID(),
	}
	opts := &lib.PodCheckpointOptions{
		TargetFile:   req.GetLocation(),
		LeaveRunning: req.GetLeaveRunning(),
	}

	_, err = s.ContainerServer.PodCheckpoint(ctx, config, opts)
	if err != nil {
		return nil, err
	}

	log.Infof(ctx, "Checkpointed pod sandbox: %s", req.GetPodSandboxId())

	return &types.CheckpointPodResponse{}, nil
}
