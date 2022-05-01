package v1

import (
	"context"

	"github.com/cri-o/cri-o/pkg/private"
	pb "k8s.io/cri-api/pkg/apis/runtime/v1"
)

func (s *service) RestoreContainer(ctx context.Context, req *pb.RestoreContainerRequest) (*pb.RestoreContainerResponse, error) {
	request := &private.RestoreContainerRequest{
		Options: &private.RestoreContainerOptions{
			PodSandboxId: req.Options.PodSandboxId,
			CommonOptions: &private.CheckpointRestoreOptions{
				Archive: req.Options.CommonOptions.ArchiveLocation,
			},
		},
	}

	response, err := s.server.RestoreContainer(ctx, request)

	if err != nil {
		return nil, err
	}

	return &pb.RestoreContainerResponse{
		Id: response.Id,
	}, nil
}
