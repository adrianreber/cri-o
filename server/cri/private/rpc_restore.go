package private

import (
	"context"

	"github.com/cri-o/cri-o/pkg/private"
)

func (s *service) RestoreContainer(ctx context.Context, req *private.RestoreContainerRequest) (*private.RestoreContainerResponse, error) {
	return s.server.RestoreContainer(ctx, req)
}
