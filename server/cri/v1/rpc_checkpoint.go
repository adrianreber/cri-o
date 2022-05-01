package v1

import (
	"context"

	"github.com/cri-o/cri-o/pkg/private"
	pb "k8s.io/cri-api/pkg/apis/runtime/v1"
)

func (s *service) CheckpointContainer(ctx context.Context, req *pb.CheckpointContainerRequest) (*pb.CheckpointContainerResponse, error) {
	request := &private.CheckpointContainerRequest {
		Id: req.ContainerId,
		Options: &private.CheckpointContainerOptions{
			CommonOptions: &private.CheckpointRestoreOptions{
				Archive: req.Options.CommonOptions.ArchiveLocation,
			},
		},
	}

	result := s.server.CheckpointContainer(ctx, request)

	return &pb.CheckpointContainerResponse{}, result
}
