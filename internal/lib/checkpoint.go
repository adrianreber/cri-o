package lib

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	metadata "github.com/checkpoint-restore/checkpointctl/lib"
	"github.com/checkpoint-restore/go-criu/v7/stats"
	"github.com/checkpoint-restore/go-criu/v7/utils"
	"github.com/containers/buildah"
	"github.com/opencontainers/cgroups"
	rspec "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/opencontainers/runtime-tools/generate"
	"go.podman.io/common/pkg/crutils"
	istorage "go.podman.io/image/v5/storage"
	"go.podman.io/storage/pkg/archive"
	"golang.org/x/sys/unix"

	"github.com/cri-o/cri-o/internal/annotations"
	"github.com/cri-o/cri-o/internal/lib/sandbox"
	"github.com/cri-o/cri-o/internal/log"
	"github.com/cri-o/cri-o/internal/oci"
	"github.com/cri-o/cri-o/internal/version"
)

// ContainerCheckpointOptions is the relevant subset of libpod.ContainerCheckpointOptions.
type ContainerCheckpointOptions struct {
	// Keep tells the API to not delete checkpoint artifacts
	Keep bool
	// KeepRunning tells the API to keep the container running
	// after writing the checkpoint to disk
	KeepRunning bool
	// TargetFile tells the API to read (or write) the checkpoint image
	// from (or to) the filename set in TargetFile
	TargetFile string
	// Pause tells the API to pause the container before checkpointing.
	// When checkpointing containers as part of a pod checkpoint, this should
	// be false since the entire pod is already frozen at the cgroup level.
	Pause bool
	// WorkPath is an optional custom directory where checkpoint metadata files
	// should be written. If empty, ctr.Dir() is used.
	WorkPath string
	// CheckpointPath is an optional custom directory where the CRIU checkpoint
	// should be written. If empty, the default checkpoint location is used.
	CheckpointPath string
}

// PodCheckpointOptions contains options for checkpointing a pod sandbox.
type PodCheckpointOptions struct {
	// Keep tells the API to not delete checkpoint artifacts
	Keep bool
	// LeaveRunning tells the API to keep the pod running
	// after writing the checkpoint to disk
	LeaveRunning bool
	// TargetFile tells the API to read (or write) the checkpoint image
	// from (or to) the filename set in TargetFile
	TargetFile string
}

// CheckpointedPodOptions contains metadata about a checkpointed pod
type CheckpointedPodOptions struct {
	// Version is the version of the pod checkpoint format
	Version int `json:"version"`
	// Containers is the list of container directory names in the checkpoint
	Containers []string `json:"containers"`
}

// ContainerCheckpoint checkpoints a running container.
func (c *ContainerServer) ContainerCheckpoint(
	ctx context.Context,
	config *metadata.ContainerConfig,
	opts *ContainerCheckpointOptions,
) (string, error) {
	ctr, err := c.LookupContainer(ctx, config.ID)
	if err != nil {
		return "", fmt.Errorf("failed to find container %s: %w", config.ID, err)
	}

	configFile := filepath.Join(ctr.BundlePath(), "config.json")

	specgen, err := generate.NewFromFile(configFile)
	if err != nil {
		return "", fmt.Errorf("not able to read config for container %q: %w", ctr.ID(), err)
	}

	cStatus := ctr.State()
	if cStatus.Status != oci.ContainerStateRunning {
		return "", fmt.Errorf("container %s is not running", ctr.ID())
	}

	// At this point the container needs to be paused. As we first checkpoint
	// the processes in the container and the container will continue to run
	// after checkpointing, there is a chance that the changed files we include
	// in the checkpoint archive might change by the now again running processes
	// in the container.
	// Assuming this uses runc/crun (the OCI runtime is currently the only one
	// supporting checkpointing), PauseContainer() will use the cgroup freezer
	// to freeze the processes. CRIU will also use the cgroup freezer to freeze
	// the processes if possible. If the cgroup is already frozen by runc/crun
	// CRIU will not change the freezer status.
	// When checkpointing as part of a pod checkpoint, the pod is already frozen
	// at the parent cgroup level, so individual container pausing is skipped.
	if opts.Pause {
		if err = c.runtime.PauseContainer(ctx, ctr); err != nil {
			return "", fmt.Errorf("failed to pause container %q before checkpointing: %w", ctr.ID(), err)
		}
	}

	defer func() {
		if err := c.runtime.UpdateContainerStatus(ctx, ctr); err != nil {
			log.Errorf(ctx, "Failed to update container status: %q: %v", ctr.ID(), err)
		}

		if opts.Pause && ctr.State().Status == oci.ContainerStatePaused {
			err := c.runtime.UnpauseContainer(ctx, ctr)
			if err != nil {
				log.Errorf(ctx, "Failed to unpause container: %q: %v", ctr.ID(), err)
			}
		}
		// container state needs to be written _after_ unpausing
		if err = c.ContainerStateToDisk(ctx, ctr); err != nil {
			log.Warnf(ctx, "Unable to write containers %s state to disk: %v", ctr.ID(), err)
		}
	}()

	// imagePath is used by CRIU to store the actual checkpoint files
	imagePath := ctr.CheckpointPath()
	if opts.CheckpointPath != "" {
		imagePath = opts.CheckpointPath
	}

	// For pod checkpointing with custom WorkPath, we need to use the default ctr.Dir()
	// for CRIU operations (to get proper SELinux labeling), then copy files to custom WorkPath
	criuWorkPath := ctr.Dir()
	finalWorkPath := ctr.Dir()
	if opts.WorkPath != "" {
		finalWorkPath = opts.WorkPath
	}

	if opts.TargetFile != "" || opts.WorkPath != "" {
		// Always prepare checkpoint export using the final work path
		if err := c.prepareCheckpointExport(ctr, finalWorkPath); err != nil {
			return "", fmt.Errorf("failed to write config dumps for container %s: %w", ctr.ID(), err)
		}
	}

	// Always use default ctr.Dir() for CRIU workPath (for SELinux labeling)
	if err := c.runtime.CheckpointContainer(ctx, ctr, specgen.Config, opts.KeepRunning, criuWorkPath, imagePath); err != nil {
		return "", fmt.Errorf("failed to checkpoint container %s: %w", ctr.ID(), err)
	}

	// If using custom WorkPath (pod checkpointing), copy CRIU-generated files from default location to custom location
	if opts.WorkPath != "" {
		// Copy dump.log and stats-dump from criuWorkPath to finalWorkPath
		filesToCopy := []string{
			metadata.DumpLogFile,
			stats.StatsDump,
		}
		for _, file := range filesToCopy {
			srcPath := filepath.Join(criuWorkPath, file)
			dstPath := filepath.Join(finalWorkPath, file)
			if err := sandbox.CopyFile(srcPath, dstPath); err != nil {
				log.Warnf(ctx, "Failed to copy %s to checkpoint directory: %v", file, err)
			}
		}
	}

	if opts.TargetFile != "" || opts.WorkPath != "" {
		if err := c.exportCheckpoint(ctx, ctr, specgen.Config, opts.TargetFile, finalWorkPath); err != nil {
			return "", fmt.Errorf("failed to write file system changes of container %s: %w", ctr.ID(), err)
		}

		// Only clean up checkpoint directory if using default path (not custom path for pod checkpoint)
		if opts.CheckpointPath == "" {
			defer func() {
				// clean up checkpoint directory
				if err := os.RemoveAll(ctr.CheckpointPath()); err != nil {
					log.Warnf(ctx, "Unable to remove checkpoint directory %s: %v", ctr.CheckpointPath(), err)
				}
			}()
		}
	}

	if !opts.KeepRunning {
		if err := c.storageRuntimeServer.StopContainer(ctx, ctr.ID()); err != nil {
			return "", fmt.Errorf("failed to unmount container %s: %w", ctr.ID(), err)
		}
	}

	// Only clean up temporary files if we're not using a custom work path
	if !opts.Keep && opts.WorkPath == "" {
		cleanup := []string{
			metadata.DumpLogFile,
			stats.StatsDump,
			metadata.ConfigDumpFile,
			metadata.SpecDumpFile,
		}
		for _, del := range cleanup {
			file := filepath.Join(ctr.Dir(), del)
			if err := os.Remove(file); err != nil {
				log.Debugf(ctx, "Unable to remove file %s", file)
			}
		}
	}

	return ctr.ID(), nil
}

// Copied from libpod/diff.go.
var containerMounts = map[string]bool{
	"/dev":               true,
	"/dev/shm":           true,
	"/proc":              true,
	"/run":               true,
	"/run/.containerenv": true,
	"/run/secrets":       true,
	"/sys":               true,
}

const bindMount = "bind"

func skipBindMount(mountPath string, specgen *rspec.Spec) bool {
	for _, m := range specgen.Mounts {
		if m.Type != bindMount {
			continue
		}

		if m.Destination == mountPath {
			return true
		}
	}

	return false
}

// getDiff returns the file system differences
// Copied from libpod/diff.go and simplified for the checkpoint use case.
func (c *ContainerServer) getDiff(ctx context.Context, id string, specgen *rspec.Spec) (rchanges []archive.Change, err error) {
	layerID, err := c.GetContainerTopLayerID(ctx, id)
	if err != nil {
		return nil, err
	}

	changes, err := c.store.Changes("", layerID)
	if err == nil {
		for _, c := range changes {
			if skipBindMount(c.Path, specgen) {
				continue
			}

			if containerMounts[c.Path] {
				continue
			}

			rchanges = append(rchanges, c)
		}
	}

	return rchanges, err
}

type ExternalBindMount struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	FileType    string `json:"file_type"`
	Permissions uint32 `json:"permissions"`
}

// prepareCheckpointExport writes the config and spec to
// JSON files for later export
// Podman: libpod/container_internal.go.
func (c *ContainerServer) prepareCheckpointExport(ctr *oci.Container, workPath string) error {
	// Use custom work path if provided, otherwise use container's directory
	if workPath == "" {
		workPath = ctr.Dir()
	}

	// save spec
	jsonPath := filepath.Join(ctr.BundlePath(), "config.json")

	g, err := generate.NewFromFile(jsonPath)
	if err != nil {
		return fmt.Errorf("generating spec for container %q failed: %w", ctr.ID(), err)
	}

	if _, err := metadata.WriteJSONFile(g.Config, workPath, metadata.SpecDumpFile); err != nil {
		return fmt.Errorf("generating spec for container %q failed: %w", ctr.ID(), err)
	}

	rootFSImageRef := ""
	if id := ctr.ImageID(); id != nil {
		rootFSImageRef = id.IDStringForOutOfProcessConsumptionOnly()
	}

	rootFSImageName := ""
	if someNameOfTheImage := ctr.SomeNameOfTheImage(); someNameOfTheImage != nil {
		rootFSImageName = someNameOfTheImage.StringForOutOfProcessConsumptionOnly()
	}

	config := &metadata.ContainerConfig{
		ID:              ctr.ID(),
		Name:            ctr.Name(),
		RootfsImage:     ctr.UserRequestedImage(),
		RootfsImageRef:  rootFSImageRef,
		RootfsImageName: rootFSImageName,
		CreatedTime:     ctr.CreatedAt(),
		OCIRuntime: func() string {
			runtimeHandler := c.GetSandbox(ctr.Sandbox()).RuntimeHandler()
			if runtimeHandler != "" {
				return runtimeHandler
			}

			return c.config.DefaultRuntime
		}(),
		CheckpointedAt: time.Now(),
		Restored:       ctr.Restore(),
	}

	if _, err := metadata.WriteJSONFile(config, workPath, metadata.ConfigDumpFile); err != nil {
		return err
	}

	// During container creation CRI-O creates all missing bind mount sources as
	// directories. This is disabled during restore as CRIU requires the bind mount
	// source to be of the same type. Directories need to be directories and regular
	// files need to be regular files. CRIU will fail to bind mount a directory on
	// a file. Especially when restoring a Kubernetes container outside of Kubernetes
	// a couple of bind mounts are files (e.g. /etc/resolv.conf). To solve this
	// CRI-O is now tracking all bind mount types in the checkpoint archive. This
	// way it is possible to know if a missing bind mount needs to be a file or a
	// directory.
	var externalBindMounts []ExternalBindMount //nolint:prealloc

	for _, m := range g.Config.Mounts {
		if containerMounts[m.Destination] {
			continue
		}

		if m.Type != bindMount {
			continue
		}

		fileInfo, err := os.Stat(m.Source)
		if err != nil {
			return fmt.Errorf("unable to stat() %q: %w", m.Source, err)
		}

		externalBindMounts = append(
			externalBindMounts,
			ExternalBindMount{
				Source:      m.Source,
				Destination: m.Destination,
				FileType: func() string {
					if fileInfo.Mode().IsDir() {
						return "directory"
					}

					return "file"
				}(),
				Permissions: uint32(fileInfo.Mode().Perm()),
			},
		)
	}

	if len(externalBindMounts) > 0 {
		if _, err := metadata.WriteJSONFile(externalBindMounts, workPath, "bind.mounts"); err != nil {
			return fmt.Errorf("error writing 'bind.mounts' for %q: %w", ctr.ID(), err)
		}
	}

	return nil
}

func (c *ContainerServer) exportCheckpoint(ctx context.Context, ctr *oci.Container, specgen *rspec.Spec, export string, workPath string) error {
	id := ctr.ID()
	// Use custom work path if provided for pod checkpoints, otherwise use container's directory
	sourcePath := ctr.Dir()
	if workPath != "" {
		sourcePath = workPath
	}

	dest := sourcePath
	log.Debugf(ctx, "Exporting checkpoint image of container %q from %q to %q", id, sourcePath, export)

	includeFiles := []string{
		stats.StatsDump,
		metadata.DumpLogFile,
		metadata.CheckpointDirectory,
		metadata.ConfigDumpFile,
		metadata.SpecDumpFile,
		"bind.mounts",
	}

	// To correctly track deleted files, let's go through the output of 'podman diff'
	rootFsChanges, err := c.getDiff(ctx, id, specgen)
	if err != nil {
		return fmt.Errorf("error exporting root file-system diff for %q: %w", id, err)
	}

	mountPoint, err := c.StorageImageServer().GetStore().Mount(id, specgen.Linux.MountLabel)
	if err != nil {
		return fmt.Errorf("not able to get mountpoint for container %q: %w", id, err)
	}

	addToTarFiles, err := crutils.CRCreateRootFsDiffTar(&rootFsChanges, mountPoint, dest)
	if err != nil {
		return err
	}

	// Put log file into checkpoint archive
	_, err = os.Stat(specgen.Annotations[annotations.LogPath])
	if err == nil {
		src, err := os.Open(specgen.Annotations[annotations.LogPath])
		if err != nil {
			return fmt.Errorf("error opening log file %q: %w", specgen.Annotations[annotations.LogPath], err)
		}

		defer src.Close()

		destLogPath := filepath.Join(dest, annotations.LogPath)

		destLog, err := os.Create(destLogPath)
		if err != nil {
			return fmt.Errorf("error opening log file %q: %w", destLogPath, err)
		}

		defer destLog.Close()

		_, err = io.Copy(destLog, src)
		if err != nil {
			return fmt.Errorf("copying log file to %q failed: %w", destLogPath, err)
		}

		addToTarFiles = append(addToTarFiles, annotations.LogPath)
	}

	// For pod checkpoints with workPath set, files are already in the right place
	// and we don't need to create a tar archive
	if export == "" {
		// No tar export needed - checkpoint is written directly to workPath
		return nil
	}

	includeFiles = append(includeFiles, addToTarFiles...)

	input, err := archive.TarWithOptions(sourcePath, &archive.TarOptions{
		// This should be configurable via api.proti
		Compression:      archive.Uncompressed,
		IncludeSourceDir: true,
		IncludeFiles:     includeFiles,
	})
	if err != nil {
		return fmt.Errorf("error reading checkpoint directory %q: %w", id, err)
	}

	// The resulting tar archive should not be readable by everyone as it contains
	// every memory page of the checkpointed processes.
	outFile, err := os.OpenFile(export, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("error creating checkpoint export file %q: %w", export, err)
	}
	defer outFile.Close()

	_, err = io.Copy(outFile, input)
	if err != nil {
		return err
	}

	for _, file := range addToTarFiles {
		os.Remove(filepath.Join(dest, file))
	}

	return nil
}

// PodCheckpoint checkpoints a running pod sandbox.
func (c *ContainerServer) PodCheckpoint(
	ctx context.Context,
	config *metadata.ContainerConfig,
	opts *PodCheckpointOptions,
) (string, error) {
	sb := c.GetSandbox(config.ID)
	if sb == nil {
		return "", fmt.Errorf("failed to find sandbox %s", config.ID)
	}

	log.Infof(ctx, "Checkpointing pod sandbox: %s", config.ID)

	containers := sb.Containers().List()

	// Create OCI image builder and mount it early to avoid copying data
	store := c.StorageImageServer().GetStore()

	builderOpts := buildah.BuilderOptions{
		FromImage: "scratch",
	}

	builder, err := buildah.NewBuilder(ctx, store, builderOpts)
	if err != nil {
		return "", fmt.Errorf("failed to create buildah builder: %w", err)
	}
	defer func() {
		if err := builder.Delete(); err != nil {
			log.Warnf(ctx, "Failed to delete buildah builder: %v", err)
		}
	}()

	// Mount the builder to get a filesystem path where we can write directly
	mountPoint, err := builder.Mount("")
	if err != nil {
		return "", fmt.Errorf("failed to mount builder: %w", err)
	}
	defer func() {
		if err := builder.Unmount(); err != nil {
			log.Warnf(ctx, "Failed to unmount builder: %v", err)
		}
	}()

	log.Infof(ctx, "Mounted OCI image at %s for pod checkpoint", mountPoint)

	// Try to atomically freeze the entire pod at the parent cgroup level
	// This may fail if the cgroup parent is "." or empty, in which case we'll
	// fall back to pausing individual containers
	cgMgr, usePodLevelFreeze := func() (cgroups.Manager, bool) {
		if sb.CgroupParent() == "" || sb.CgroupParent() == "." {
			log.Infof(ctx, "Cgroup parent is %q, will pause containers individually", sb.CgroupParent())
			return nil, false
		}

		mgr, err := c.config.CgroupManager().SandboxCgroupManager(sb.CgroupParent(), sb.ID())
		if err != nil {
			log.Warnf(ctx, "Failed to get sandbox cgroup manager (will pause containers individually): %v", err)
			return nil, false
		}

		// In theory his should be:

		// Atomically freeze the entire pod sandbox by freezing the parent cgroup.
		// This freezes all containers in the pod simultaneously.
		// if err := mgr.Freeze(cgroups.Frozen); err != nil {
		// 	log.Warnf(ctx, "Failed to freeze pod sandbox at cgroup level (will pause containers individually): %v", err)
		//	return mgr, false
		// }
		//
		// return mgr, true
		// log.Infof(ctx, "Successfully froze pod sandbox %s at cgroup level", sb.ID())

		// but this does not yet work in combination with CRIU.
		// If CRIU is called from runc, runc will pass the cgroup of the
		// container to CRIU and also set FreezeCgroup to true.
		// If we are, however, freezing the container via the parent cgroup freezer
		// this does not work because CRIU is unaware that the parent
		// cgroup is already frozen and just hang in the parasite code
		// because it is frozen. We need a solution in CRIU how to
		// handle a frozen parent cgroup.
		log.Infof(ctx, "Did not freeze pod sandbox %s at cgroup level (CRIU is not ready)", sb.ID())
		return mgr, false
	}()

	defer func() {
		// Unfreeze the sandbox if we're keeping it running and we froze at pod level
		if usePodLevelFreeze && opts.LeaveRunning && cgMgr != nil {
			if err := cgMgr.Freeze(cgroups.Thawed); err != nil {
				log.Errorf(ctx, "Failed to unfreeze pod sandbox %s: %v", sb.ID(), err)
			}
		}
	}()

	// Pause all containers first (if not frozen at pod level)
	// This ensures all containers are paused before any checkpointing starts
	pausedContainers, err := func() (map[string]bool, error) {
		paused := make(map[string]bool)
		if usePodLevelFreeze {
			return paused, nil
		}

		for _, ctr := range containers {
			log.Infof(ctx, "Pausing container %s before pod checkpoint", ctr.ID())
			if err := c.runtime.PauseContainer(ctx, ctr); err != nil {
				// Best effort unpause any already paused containers
				for pausedID := range paused {
					if pausedCtr, err := c.LookupContainer(ctx, pausedID); err == nil {
						if err := c.runtime.UnpauseContainer(ctx, pausedCtr); err != nil {
							log.Errorf(ctx, "Failed to unpause container %s: %v", pausedID, err)
						}
					}
				}
				return paused, fmt.Errorf("failed to pause container %s: %w", ctr.ID(), err)
			}
			paused[ctr.ID()] = true
		}
		return paused, nil
	}()
	if err != nil {
		return "", err
	}

	// Defer unpausing all containers (if we paused them)
	defer func() {
		if !usePodLevelFreeze {
			for containerID := range pausedContainers {
				ctr, err := c.LookupContainer(ctx, containerID)
				if err != nil {
					log.Errorf(ctx, "Failed to lookup container %s for unpausing: %v", containerID, err)
					continue
				}
				if err := c.runtime.UpdateContainerStatus(ctx, ctr); err != nil {
					log.Errorf(ctx, "Failed to update container status for %s: %v", containerID, err)
				}
				if ctr.State().Status == oci.ContainerStatePaused {
					if err := c.runtime.UnpauseContainer(ctx, ctr); err != nil {
						log.Errorf(ctx, "Failed to unpause container %s: %v", containerID, err)
					}
				}
			}
		}
	}()

	// Track container names for pod metadata
	var containerNames []string

	// Checkpoint all containers directly into the mounted OCI image
	// All containers are already paused/frozen at this point
	for _, ctr := range containers {
		log.Infof(ctx, "Checkpointing container %s in pod %s", ctr.ID(), sb.ID())

		containerConfig := &metadata.ContainerConfig{
			ID: ctr.ID(),
		}

		// Create directory structure in the mounted image for this container
		// Format: <containerID>-<containerName>
		containerDirName := fmt.Sprintf("%s-%s", ctr.ID(), ctr.Name())
		containerDir := filepath.Join(mountPoint, containerDirName)
		if err := os.MkdirAll(containerDir, 0o755); err != nil {
			return "", fmt.Errorf("failed to create container directory %s: %w", containerDir, err)
		}

		// Create checkpoint subdirectory for CRIU checkpoint data
		checkpointDir := filepath.Join(containerDir, "checkpoint")
		if err := os.MkdirAll(checkpointDir, 0o755); err != nil {
			return "", fmt.Errorf("failed to create checkpoint directory %s: %w", checkpointDir, err)
		}

		// Create checkpoint options for this container
		// Point WorkPath and CheckpointPath to directories in the mounted image
		containerOpts := &ContainerCheckpointOptions{
			Keep:        opts.Keep,
			KeepRunning: opts.LeaveRunning,
			// TargetFile is empty - we write directly to WorkPath, no tar needed
			TargetFile: "",
			// Don't pause again - containers are already paused/frozen
			Pause:          false,
			WorkPath:       containerDir,
			CheckpointPath: checkpointDir,
		}

		_, err := c.ContainerCheckpoint(ctx, containerConfig, containerOpts)
		if err != nil {
			return "", fmt.Errorf("failed to checkpoint container %s in pod %s: %w", ctr.ID(), sb.ID(), err)
		}

		containerNames = append(containerNames, containerDirName)
	}

	// Write pod metadata to the mounted image
	// This will be used during restore to recreate the pod
	if err := c.writePodCheckpointMetadata(ctx, sb, mountPoint, containerNames); err != nil {
		return "", fmt.Errorf("failed to write pod metadata: %w", err)
	}

	// Set annotations for the checkpoint image
	// Start with pod-specific annotations
	podAnnotations := map[string]string{
		metadata.CheckpointAnnotationPod:       sb.Metadata().GetName(),
		metadata.CheckpointAnnotationPodID:     sb.ID(),
		metadata.CheckpointAnnotationNamespace: sb.Metadata().GetNamespace(),
		metadata.CheckpointAnnotationPodUID:    sb.Metadata().GetUid(),
	}

	// Get system and checkpoint metadata annotations
	systemAnnotations := getCheckpointAnnotations()

	// Merge all annotations
	for key, value := range podAnnotations {
		builder.SetAnnotation(key, value)
	}
	for key, value := range systemAnnotations {
		builder.SetAnnotation(key, value)
	}

	// Parse the target file as a storage reference
	// This supports various formats: "name:tag", "name@digest", or just "name"
	imageRef, err := istorage.Transport.ParseStoreReference(store, opts.TargetFile)
	if err != nil {
		return "", fmt.Errorf("failed to parse storage reference for %q: %w", opts.TargetFile, err)
	}

	// Commit the image
	commitOptions := buildah.CommitOptions{
		Squash: true,
	}

	imageID, _, _, err := builder.Commit(ctx, imageRef, commitOptions)
	if err != nil {
		return "", fmt.Errorf("failed to commit checkpoint image: %w", err)
	}

	log.Infof(ctx, "Successfully created pod checkpoint image %s with ID %s", opts.TargetFile, imageID)
	return sb.ID(), nil
}

// writePodCheckpointMetadata writes pod metadata files to the checkpoint image
// This metadata is needed to restore the pod
func (c *ContainerServer) writePodCheckpointMetadata(ctx context.Context, sb *sandbox.Sandbox, mountPoint string, containerNames []string) error {
	// Create CheckpointedPodOptions containing the list of containers
	checkpointedPodOptions := &CheckpointedPodOptions{
		Version:    1,
		Containers: containerNames,
	}

	if _, err := metadata.WriteJSONFile(checkpointedPodOptions, mountPoint, metadata.PodOptionsFile); err != nil {
		return fmt.Errorf("error writing pod options file: %w", err)
	}

	log.Infof(ctx, "Wrote pod options with %d containers to checkpoint image", len(containerNames))
	return nil
}

// getCheckpointAnnotations gathers system and checkpoint metadata annotations
func getCheckpointAnnotations() map[string]string {
	annotations := make(map[string]string)

	// Engine name and version
	annotations[metadata.CheckpointAnnotationEngine] = "cri-o"
	annotations[metadata.CheckpointAnnotationEngineVersion] = version.Version

	// Host architecture
	annotations[metadata.CheckpointAnnotationHostArch] = runtime.GOARCH

	// Get CRIU version
	criuVersion, err := utils.GetCriuVersion()
	if err == nil {
		annotations[metadata.CheckpointAnnotationCriuVersion] = strconv.Itoa(criuVersion)
	}

	// Get kernel version using uname
	var utsname unix.Utsname
	if err := unix.Uname(&utsname); err == nil {
		// Convert []int8 to string
		release := make([]byte, 0, len(utsname.Release))
		for _, b := range utsname.Release {
			if b == 0 {
				break
			}
			release = append(release, byte(b))
		}
		annotations[metadata.CheckpointAnnotationHostKernel] = string(release)
	}

	// Detect cgroup version
	cgroupVersion := "v1"
	if cgroups.IsCgroup2UnifiedMode() {
		cgroupVersion = "v2"
	}
	annotations[metadata.CheckpointAnnotationCgroupVersion] = cgroupVersion

	// Read distribution information from /etc/os-release
	if file, err := os.Open("/etc/os-release"); err == nil {
		defer file.Close()
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "NAME=") {
				name := strings.TrimPrefix(line, "NAME=")
				name = strings.Trim(name, `"`)
				annotations[metadata.CheckpointAnnotationDistributionName] = name
			} else if strings.HasPrefix(line, "VERSION=") || strings.HasPrefix(line, "VERSION_ID=") {
				ver := strings.TrimPrefix(line, "VERSION=")
				if strings.HasPrefix(line, "VERSION_ID=") {
					ver = strings.TrimPrefix(line, "VERSION_ID=")
				}
				ver = strings.Trim(ver, `"`)
				annotations[metadata.CheckpointAnnotationDistributionVersion] = ver
			}
		}
	}

	return annotations
}
