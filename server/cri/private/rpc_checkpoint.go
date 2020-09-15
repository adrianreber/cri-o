package private

import (
	"context"

	"github.com/cri-o/cri-o/pkg/private"
)

func (s *service) CheckpointContainer(ctx context.Context, req *private.CheckpointContainerRequest) (*private.CheckpointContainerResponse, error) {
	return &private.CheckpointContainerResponse{}, s.server.CheckpointContainer(ctx, req)
}
