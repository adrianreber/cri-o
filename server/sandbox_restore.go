package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	metadata "github.com/checkpoint-restore/checkpointctl/lib"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	types "k8s.io/cri-api/pkg/apis/runtime/v1"

	"github.com/cri-o/cri-o/internal/lib"
	"github.com/cri-o/cri-o/internal/log"
)

// RestorePod restores a pod sandbox from a checkpoint.
func (s *Server) RestorePod(ctx context.Context, req *types.RestorePodRequest) (*types.RestorePodResponse, error) {
	if !s.config.CheckpointRestore() {
		return nil, errors.New("checkpoint/restore support not available")
	}

	// Validate that location is provided
	if req.GetLocation() == "" {
		return nil, status.Error(codes.InvalidArgument, "location is required for pod restore")
	}

	log.Infof(ctx, "Restoring pod from checkpoint: %s", req.GetLocation())

	// Check if the location refers to a pod checkpoint OCI image
	restoreStorageImageID, podName, podNamespace, oldPodID, podUID, err := s.checkIfPodCheckpointOCIImage(ctx, req.GetLocation())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to check checkpoint image: %v", err)
	}

	if restoreStorageImageID == nil {
		return nil, status.Errorf(codes.InvalidArgument, "location %q does not refer to a pod checkpoint image", req.GetLocation())
	}

	log.Infof(ctx, "Found pod checkpoint for %q (namespace: %s, old ID: %s, UID: %s) in %s", podName, podNamespace, oldPodID, podUID, req.GetLocation())

	// Mount the checkpoint image to read its contents
	imageIDString := restoreStorageImageID.IDStringForOutOfProcessConsumptionOnly()
	store := s.ContainerServer.StorageImageServer().GetStore()

	mountPoint, err := store.MountImage(imageIDString, nil, "")
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to mount checkpoint image: %v", err)
	}
	defer func() {
		if _, err := store.UnmountImage(imageIDString, true); err != nil {
			log.Errorf(ctx, "Failed to unmount checkpoint image: %v", err)
		}
	}()

	log.Debugf(ctx, "Mounted checkpoint image at %s", mountPoint)

	// Read pod.options file to get the list of containers
	checkpointedPodOptions := &lib.CheckpointedPodOptions{}
	if _, err := metadata.ReadJSONFile(checkpointedPodOptions, mountPoint, metadata.PodOptionsFile); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to read pod options: %v", err)
	}

	if checkpointedPodOptions.Version != 1 {
		return nil, status.Errorf(codes.InvalidArgument, "unsupported pod checkpoint version %d", checkpointedPodOptions.Version)
	}

	log.Infof(ctx, "Pod checkpoint contains %d containers", len(checkpointedPodOptions.Containers))

	if len(checkpointedPodOptions.Containers) == 0 {
		return nil, status.Error(codes.InvalidArgument, "pod checkpoint contains no containers")
	}

	// Construct a PodSandboxConfig from checkpoint metadata
	// Use the provided config from request if available, otherwise construct from checkpoint
	var podConfig *types.PodSandboxConfig
	if req.GetConfig() != nil {
		podConfig = req.GetConfig()
		log.Infof(ctx, "Using provided PodSandboxConfig from request")
	} else {
		// Extract pod metadata from checkpoint annotations
		podConfig = &types.PodSandboxConfig{
			Metadata: &types.PodSandboxMetadata{
				Name:      podName,
				Namespace: podNamespace,
				Uid:       podUID,
			},
			Labels:      make(map[string]string),
			Annotations: make(map[string]string),
		}
		log.Infof(ctx, "Constructed minimal PodSandboxConfig from checkpoint metadata (UID: %s)", podUID)
	}

	// Apply label/annotation overrides from request if provided
	if req.GetLabels() != nil {
		if podConfig.Labels == nil {
			podConfig.Labels = make(map[string]string)
		}
		for k, v := range req.GetLabels() {
			podConfig.Labels[k] = v
		}
	}
	if req.GetAnnotations() != nil {
		if podConfig.Annotations == nil {
			podConfig.Annotations = make(map[string]string)
		}
		for k, v := range req.GetAnnotations() {
			podConfig.Annotations[k] = v
		}
	}

	// Create a new pod sandbox using RunPodSandbox
	log.Infof(ctx, "Creating new pod sandbox for restored pod")

	runPodReq := &types.RunPodSandboxRequest{
		Config: podConfig,
	}

	sandboxResp, err := s.RunPodSandbox(ctx, runPodReq)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create pod sandbox: %v", err)
	}

	newPodID := sandboxResp.GetPodSandboxId()
	log.Infof(ctx, "Created new pod sandbox with ID: %s", newPodID)

	// Get the sandbox object for container restoration
	sb := s.GetSandbox(newPodID)
	if sb == nil {
		return nil, status.Errorf(codes.Internal, "failed to get created sandbox %s", newPodID)
	}

	// Now restore each container into the new sandbox
	log.Infof(ctx, "Restoring %d containers into pod %s", len(checkpointedPodOptions.Containers), newPodID)

	var restoredContainers []string

	for i, containerDirName := range checkpointedPodOptions.Containers {
		containerDir := filepath.Join(mountPoint, containerDirName)

		// Read container metadata
		var containerConfig metadata.ContainerConfig
		if _, err := metadata.ReadJSONFile(&containerConfig, containerDir, metadata.ConfigDumpFile); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to read config for container %s: %v", containerDirName, err)
		}

		log.Infof(ctx, "Restoring container %d/%d: %s (name: %s)", i+1, len(checkpointedPodOptions.Containers), containerConfig.ID, containerConfig.Name)

		// Construct a ContainerConfig for CRImportCheckpoint
		// The Image field will point to the containerDir, which contains the checkpoint data
		// CRImportCheckpoint now supports directory-based checkpoints
		createConfig := &types.ContainerConfig{
			Metadata: &types.ContainerMetadata{
				Name:    containerConfig.Name,
				Attempt: 0,
			},
			Image: &types.ImageSpec{
				Image: containerDir, // Point to the directory containing checkpoint data
			},
			Linux: &types.LinuxContainerConfig{
				Resources:       &types.LinuxContainerResources{},
				SecurityContext: &types.LinuxContainerSecurityContext{},
			},
		}

		// Call CRImportCheckpoint which will:
		// 1. Detect that containerDir is a directory (new feature)
		// 2. Use it directly without mounting or extracting
		// 3. Create the container structure
		// 4. Restore the container from the checkpoint data
		containerID, err := s.CRImportCheckpoint(ctx, createConfig, sb, podUID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to restore container %s: %v", containerConfig.Name, err)
		}

		// Debug: List checkpoint directory contents
		if entries, err := os.ReadDir(containerDir); err == nil {
			log.Debugf(ctx, "Checkpoint directory %s contents:", containerDir)
			for _, entry := range entries {
				info, _ := entry.Info()
				if info != nil {
					log.Debugf(ctx, "  - %s (size: %d bytes, dir: %v)", entry.Name(), info.Size(), entry.IsDir())
				} else {
					log.Debugf(ctx, "  - %s (dir: %v)", entry.Name(), entry.IsDir())
				}
			}
		} else {
			log.Debugf(ctx, "Failed to list checkpoint directory %s: %v", containerDir, err)
		}

		log.Infof(ctx, "Successfully restored container %s with ID %s", containerConfig.Name, containerID)
		restoredContainers = append(restoredContainers, containerID)
	}

	log.Infof(ctx, "Successfully imported %d containers into pod %s: %v", len(restoredContainers), newPodID, restoredContainers)

	// Second loop: Start each container to trigger the actual CRIU restore
	// Containers are marked for restore, so StartContainer will call ContainerRestore
	log.Infof(ctx, "Starting CRIU restore for %d containers in pod %s", len(restoredContainers), newPodID)

	for i, containerID := range restoredContainers {
		log.Infof(ctx, "Starting container %d/%d: %s", i+1, len(restoredContainers), containerID)

		startReq := &types.StartContainerRequest{
			ContainerId: containerID,
		}

		_, err := s.StartContainer(ctx, startReq)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to start/restore container %s: %v", containerID, err)
		}

		log.Infof(ctx, "Successfully restored and started container %s", containerID)
	}

	log.Infof(ctx, "Successfully restored pod %s with %d containers: %v", newPodID, len(restoredContainers), restoredContainers)

	return &types.RestorePodResponse{
		PodSandboxId: newPodID,
	}, nil
}
