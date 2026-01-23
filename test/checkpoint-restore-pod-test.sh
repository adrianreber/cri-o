#!/usr/bin/env bash

# Pod checkpoint/restore test script for CRI-O
# Based on containerd's checkpoint-restore-cri-test.sh
# Tests pod-level checkpoint and restore functionality with multiple containers

set -eu -o pipefail

# Source common test utilities and variables
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=test/common.sh
source "${SCRIPT_DIR}/common.sh"

# Test-specific configuration
CRIO_SOCKET="/var/run/crio/crio.sock"
TEST_IMAGE="${TEST_IMAGE:-quay.io/crio/busybox:latest}"
CHECKPOINT_IMAGE="localhost/checkpoint-pod:test-$$"

# Test state
CRIO_PID=""
POD_ID=""
CONTAINER_ID=""
CONTAINER2_ID=""
RESTORED_POD_ID=""
TEST_DIR=""

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Logging functions
log() {
    echo -e "${GREEN}[INFO]${NC} $*"
}

warn() {
    echo -e "${YELLOW}[WARN]${NC} $*"
}

error() {
    echo -e "${RED}[ERROR]${NC} $*" >&2
}

# Cleanup function
cleanup() {
    local exit_code=$?

    log "Cleaning up test environment..."

    # Stop and remove all pods
    if [ -n "${CRICTL_BINARY}" ] && [ -S "${CRIO_SOCKET}" ]; then
        "${CRICTL_BINARY}" -t 5s rmp -fa 2>/dev/null || true
    fi

    # Remove checkpoint image
    if [ -n "${CHECKPOINT_IMAGE}" ]; then
        buildah rmi "${CHECKPOINT_IMAGE}" 2>/dev/null || true
    fi

    # Stop CRI-O
    if [ -n "${CRIO_PID}" ]; then
        log "Stopping CRI-O (PID: ${CRIO_PID})..."
        # Send SIGTERM first
        if [ -d "/proc/${CRIO_PID}" ]; then
            kill "${CRIO_PID}" 2>/dev/null || true

            # Wait up to 5 seconds for graceful shutdown
            local count=0
            while [ -d "/proc/${CRIO_PID}" ] && [ ${count} -lt 50 ]; do
                sleep 0.1
                count=$((count + 1))
            done

            # If still running, force kill
            if [ -d "/proc/${CRIO_PID}" ]; then
                warn "CRI-O did not stop gracefully, forcing termination..."
                kill -9 "${CRIO_PID}" 2>/dev/null || true
                sleep 0.5
            fi
        fi
    fi

    if [ ${exit_code} -eq 0 ]; then
        log "Test completed successfully!"
        # Clean up test directory on success
        if [ -n "${TEST_DIR}" ] && [ -d "${TEST_DIR}" ]; then
            rm -rf "${TEST_DIR}" 2>/dev/null || true
        fi
    else
        error "Test failed with exit code ${exit_code}"
        # Show CRI-O logs on failure
        if [ -n "${TEST_DIR}" ] && [ -f "${TEST_DIR}/crio.log" ]; then
            error "CRI-O logs (last 50 lines):"
            tail -50 "${TEST_DIR}/crio.log" 2>&1 >&2 || true
            error "Full logs available at: ${TEST_DIR}/crio.log"
        fi
    fi

    # Remove socket
    rm -f "${CRIO_SOCKET}" 2>/dev/null || true

    exit ${exit_code}
}

trap cleanup EXIT

# Check prerequisites
check_prerequisites() {
    log "Checking prerequisites..."

    # Check if running as root
    if [ "$(id -u)" -ne 0 ]; then
        error "This script must be run as root"
        exit 1
    fi

    # Build checkcriu if needed
    if [ ! -f "${CHECKCRIU_BINARY}" ]; then
        log "Building checkcriu tool..."
        (cd "${SCRIPT_DIR}/checkcriu" && go build -o checkcriu checkcriu.go)
    fi

    # Check for CRIU
    log "Checking for CRIU..."
    if ! "${CHECKCRIU_BINARY}"; then
        error "CRIU is not available or version is too old"
        error "Pod checkpointing requires CRIU with pod checkpoint support"
        exit 1
    fi

    # Check for CRI-O binary
    if [ ! -x "${CRIO_BINARY_PATH}" ]; then
        error "CRI-O binary not found at ${CRIO_BINARY_PATH}"
        error "Please build CRI-O first: make bin/crio"
        exit 1
    fi

    # Check for pinns binary
    if [ ! -x "${PINNS_BINARY_PATH}" ]; then
        error "pinns binary not found at ${PINNS_BINARY_PATH}"
        error "Please build pinns first: make bin/pinns"
        exit 1
    fi

    # Check for crictl
    if [ -z "${CRICTL_BINARY}" ] || [ ! -x "${CRICTL_BINARY}" ]; then
        error "crictl binary not found"
        error "Please install crictl or build from cri-tools: (cd ../cri-tools && make crictl)"
        exit 1
    fi

    # Check if crictl supports pod checkpoint/restore commands
    # Note: We capture output first to avoid SIGPIPE issues with grep -q and pipefail
    log "Checking crictl pod checkpoint/restore support..."
    local crictl_help
    crictl_help=$("${CRICTL_BINARY}" --help 2>&1 || true)

    if ! echo "${crictl_help}" | grep -q "checkpointp"; then
        error "crictl does not support 'checkpointp' command"
        error "This test requires crictl with pod checkpoint support"
        error "Please build crictl from cri-tools with pod checkpoint support"
        exit 1
    fi

    if ! echo "${crictl_help}" | grep -q "restorep"; then
        error "crictl does not support 'restorep' command"
        error "This test requires crictl with pod restore support"
        error "Please build crictl from cri-tools with pod restore support"
        exit 1
    fi
    log "crictl supports pod checkpoint/restore commands"

    # Check for buildah
    if ! command -v buildah >/dev/null 2>&1; then
        error "buildah is required for checkpoint image management"
        exit 1
    fi

    # Check for jq
    if ! command -v jq >/dev/null 2>&1; then
        error "jq is required for JSON parsing"
        exit 1
    fi

    log "All prerequisites satisfied"
}

# Start CRI-O server
start_crio() {
    log "Starting CRI-O server..."

    # Create test directory for CRI-O runtime
    TEST_DIR="$(mktemp -d /tmp/crio-test.XXXXXX)"
    log "Test directory: ${TEST_DIR}"

    # Create crictl config to suppress warnings
    cat > "${TEST_DIR}/crictl.yaml" <<EOF
runtime-endpoint: unix://${CRIO_SOCKET}
image-endpoint: unix://${CRIO_SOCKET}
timeout: 20
EOF

    # Export crictl configuration
    export CRI_CONFIG_FILE="${TEST_DIR}/crictl.yaml"

    # Start CRI-O in background (output redirected to log file)
    "${CRIO_BINARY_PATH}" \
        --pinns-path "${PINNS_BINARY_PATH}" \
        --default-runtime runc \
        --log-level debug \
        --enable-pod-events \
        > "${TEST_DIR}/crio.log" 2>&1 &

    CRIO_PID=$!
    log "CRI-O started with PID ${CRIO_PID} (logs: ${TEST_DIR}/crio.log)"

    # Wait for CRI-O socket
    log "Waiting for CRI-O socket..."
    local timeout=30
    local count=0
    while [ ! -S "${CRIO_SOCKET}" ]; do
        sleep 1
        count=$((count + 1))
        if [ ${count} -ge ${timeout} ]; then
            error "Timeout waiting for CRI-O socket"
            exit 1
        fi
    done

    log "CRI-O socket ready"
}

# Pull test image
pull_test_image() {
    log "Pulling test image: ${TEST_IMAGE}"
    "${CRICTL_BINARY}" pull "${TEST_IMAGE}"
}

# Create pod configuration
create_pod_config() {
    local config_file="${TEST_DIR}/pod-config.json"

    cat > "${config_file}" <<EOF
{
    "metadata": {
        "name": "test-pod-$$",
        "uid": "test-pod-uid-$$",
        "namespace": "default"
    },
    "log_directory": "${TEST_DIR}"
}
EOF

    echo "${config_file}"
}

# Create container configuration
create_container_config() {
    local container_name="$1"
    local config_file="${TEST_DIR}/${container_name}-config.json"

    cat > "${config_file}" <<EOF
{
    "metadata": {
        "name": "${container_name}"
    },
    "image": {
        "image": "${TEST_IMAGE}"
    },
    "command": [
        "/bin/sh",
        "-c",
        "echo 'Container ${container_name} started' > /tmp/${container_name}.txt && sleep 3600"
    ],
    "log_path": "${container_name}.log",
    "linux": {}
}
EOF

    echo "${config_file}"
}

# Test pod checkpoint and restore
test_pod_checkpoint_restore() {
    log "=== Testing Pod Checkpoint and Restore ==="

    # Create configurations
    local pod_config
    pod_config="$(create_pod_config)"

    local container1_config
    container1_config="$(create_container_config "container1")"

    local container2_config
    container2_config="$(create_container_config "container2")"

    # Create pod
    log "Creating pod..."
    POD_ID=$("${CRICTL_BINARY}" runp "${pod_config}")
    log "Pod created: ${POD_ID}"

    # Create first container
    log "Creating first container..."
    CONTAINER_ID=$("${CRICTL_BINARY}" create "${POD_ID}" "${container1_config}" "${pod_config}")
    log "Container 1 created: ${CONTAINER_ID}"

    # Create second container
    log "Creating second container..."
    CONTAINER2_ID=$("${CRICTL_BINARY}" create "${POD_ID}" "${container2_config}" "${pod_config}")
    log "Container 2 created: ${CONTAINER2_ID}"

    # Start both containers
    log "Starting containers..."
    "${CRICTL_BINARY}" start "${CONTAINER_ID}"
    "${CRICTL_BINARY}" start "${CONTAINER2_ID}"

    # Wait for containers to be running
    log "Waiting for containers to be ready..."
    sleep 3

    # Verify containers are running
    log "Verifying containers are running..."
    "${CRICTL_BINARY}" ps -a

    local container1_state
    container1_state=$("${CRICTL_BINARY}" inspect "${CONTAINER_ID}" 2>/dev/null | jq -r '.status.state' || echo "UNKNOWN")

    local container2_state
    container2_state=$("${CRICTL_BINARY}" inspect "${CONTAINER2_ID}" 2>/dev/null | jq -r '.status.state' || echo "UNKNOWN")

    if [ "${container1_state}" != "CONTAINER_RUNNING" ]; then
        error "Container 1 is not running (state: ${container1_state})"
        exit 1
    fi

    if [ "${container2_state}" != "CONTAINER_RUNNING" ]; then
        error "Container 2 is not running (state: ${container2_state})"
        exit 1
    fi

    log "Both containers are running"

    # Checkpoint the pod
    log "Checkpointing pod to image: ${CHECKPOINT_IMAGE}"
    "${CRICTL_BINARY}" -t 20s checkpointp --export="${CHECKPOINT_IMAGE}" "${POD_ID}"

    # Verify checkpoint image was created
    # Note: Capture output first to avoid SIGPIPE issues with grep -q and pipefail
    log "Verifying checkpoint image..."
    local buildah_images
    buildah_images=$(buildah images 2>&1 || true)
    if ! echo "${buildah_images}" | grep -q "checkpoint-pod"; then
        error "Checkpoint image was not created"
        exit 1
    fi
    log "Checkpoint image created successfully"

    # Inspect checkpoint image annotations
    log "Inspecting checkpoint image annotations..."
    buildah inspect "${CHECKPOINT_IMAGE}" 2>/dev/null | jq -r '.OCIv1.config.Labels | to_entries[] | select(.key | startswith("org.criu.checkpoint")) | "\(.key)=\(.value)"' || true

    # Remove all pods
    log "Removing all pods..."
    "${CRICTL_BINARY}" -t 5s rmp -fa

    # Verify pod is removed
    sleep 2
    local pods_list
    pods_list=$("${CRICTL_BINARY}" pods 2>&1 || true)
    if echo "${pods_list}" | grep -q "${POD_ID}"; then
        error "Pod was not removed"
        exit 1
    fi
    log "Pod removed successfully"

    # Restore the pod
    log "Restoring pod from image: ${CHECKPOINT_IMAGE}"
    RESTORED_POD_ID=$("${CRICTL_BINARY}" -t 20s restorep -l "${CHECKPOINT_IMAGE}")
    log "Pod restored: ${RESTORED_POD_ID}"

    # Wait for containers to be restored
    log "Waiting for containers to be restored..."
    sleep 5

    # Verify restored pod and containers
    log "Verifying restored pod and containers..."
    "${CRICTL_BINARY}" pods
    "${CRICTL_BINARY}" ps -a

    # Get restored container IDs
    local restored_containers
    restored_containers=$("${CRICTL_BINARY}" ps -a --pod="${RESTORED_POD_ID}" -q)

    local container_count
    container_count=$(echo "${restored_containers}" | wc -w)

    if [ "${container_count}" -ne 2 ]; then
        error "Expected 2 containers in restored pod, found ${container_count}"
        exit 1
    fi

    log "Found ${container_count} containers in restored pod"

    # Verify both containers are running
    for ctr_id in ${restored_containers}; do
        local state
        state=$("${CRICTL_BINARY}" inspect "${ctr_id}" 2>/dev/null | jq -r '.status.state' || echo "UNKNOWN")

        if [ "${state}" != "CONTAINER_RUNNING" ]; then
            error "Restored container ${ctr_id} is not running (state: ${state})"
            exit 1
        fi

        local name
        name=$("${CRICTL_BINARY}" inspect "${ctr_id}" 2>/dev/null | jq -r '.status.metadata.name' || echo "unknown")
        log "Container ${name} (${ctr_id}) is running"
    done

    log "Pod checkpoint and restore test completed successfully!"
}

# Main test execution
main() {
    log "=== CRI-O Pod Checkpoint/Restore Test ==="
    log "Test timestamp: $(date)"
    log "CRI-O binary: ${CRIO_BINARY_PATH}"
    log "crictl binary: ${CRICTL_BINARY}"
    log "pinns binary: ${PINNS_BINARY_PATH}"
    log "Test image: ${TEST_IMAGE}"

    check_prerequisites
    start_crio
    pull_test_image
    test_pod_checkpoint_restore

    log "All tests passed!"
}

main "$@"
