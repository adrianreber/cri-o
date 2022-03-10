package v1

import (
	"context"

	pb "k8s.io/cri-api/pkg/apis/runtime/v1"
)

func (s *service) RestoreContainer(ctx context.Context, req *pb.RestoreContainerRequest) (*pb.RestoreContainerResponse, error) {
	return &pb.RestoreContainerResponse{}, nil
}
