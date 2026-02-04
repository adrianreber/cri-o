package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

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

	// Build a map of container name -> ContainerConfig from the request for quick lookup
	containerConfigMap := make(map[string]*types.ContainerConfig)
	if req.GetContainerConfigs() != nil {
		log.Infof(ctx, "Processing %d container configs from RestorePodRequest", len(req.GetContainerConfigs()))
		for _, cc := range req.GetContainerConfigs() {
			if cc.GetMetadata() != nil && cc.GetMetadata().GetName() != "" {
				containerConfigMap[cc.GetMetadata().GetName()] = cc
				log.Debugf(ctx, "Mapped container config for container: %s", cc.GetMetadata().GetName())
			}
		}
	} else {
		log.Infof(ctx, "No container configs provided in RestorePodRequest")
	}

	var restoredContainers []string

	for i, containerDirName := range checkpointedPodOptions.Containers {
		containerDir := filepath.Join(mountPoint, containerDirName)

		// Read container metadata
		var containerConfig metadata.ContainerConfig
		if _, err := metadata.ReadJSONFile(&containerConfig, containerDir, metadata.ConfigDumpFile); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to read config for container %s: %v", containerDirName, err)
		}

		log.Infof(ctx, "Restoring container %d/%d: %s (name: %s)", i+1, len(checkpointedPodOptions.Containers), containerConfig.ID, containerConfig.Name)

		// Extract the simple container name from the CRI name format
		// CRI name format: k8s_{containerName}_{podName}_{namespace}_{podUID}_{attempt}
		// We need just the {containerName} part to match with the provided configs
		var simpleContainerName string
		if strings.HasPrefix(containerConfig.Name, "k8s_") {
			parts := strings.SplitN(containerConfig.Name, "_", 3)
			if len(parts) >= 2 {
				simpleContainerName = parts[1]
				log.Debugf(ctx, "Extracted simple name '%s' from CRI name '%s'", simpleContainerName, containerConfig.Name)
			} else {
				simpleContainerName = containerConfig.Name
				log.Debugf(ctx, "Could not parse CRI name '%s', using as-is", containerConfig.Name)
			}
		} else {
			simpleContainerName = containerConfig.Name
		}

		// Look up the ContainerConfig provided by kubelet for this container
		var providedConfig *types.ContainerConfig
		if cc, found := containerConfigMap[simpleContainerName]; found {
			providedConfig = cc
			log.Debugf(ctx, "Found provided ContainerConfig for container %s (simple name: %s)", containerConfig.Name, simpleContainerName)
		} else {
			log.Debugf(ctx, "No provided ContainerConfig found for container %s (simple name: %s)", containerConfig.Name, simpleContainerName)
		}

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

		// Apply labels, annotations, and other metadata from the provided ContainerConfig
		// These are critical for Kubernetes to identify and track the containers
		if providedConfig != nil {
			if providedConfig.GetLabels() != nil {
				createConfig.Labels = providedConfig.GetLabels()
				log.Debugf(ctx, "Applying %d labels to container %s", len(providedConfig.GetLabels()), containerConfig.Name)
			}
			if providedConfig.GetAnnotations() != nil {
				createConfig.Annotations = providedConfig.GetAnnotations()
				log.Debugf(ctx, "Applying %d annotations to container %s", len(providedConfig.GetAnnotations()), containerConfig.Name)
			}
		}

		// Apply mounts from the provided ContainerConfig if available
		if providedConfig != nil && providedConfig.GetMounts() != nil {
			createConfig.Mounts = providedConfig.GetMounts()
			log.Infof(ctx, "Applying %d mounts to container %s", len(providedConfig.GetMounts()), containerConfig.Name)
			for idx, mount := range providedConfig.GetMounts() {
				log.Debugf(ctx, "  Mount %d: %s -> %s (readonly: %v)", idx, mount.GetHostPath(), mount.GetContainerPath(), mount.GetReadonly())
			}
		} else {
			log.Debugf(ctx, "No mounts to apply for container %s", containerConfig.Name)
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
