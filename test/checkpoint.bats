#!/usr/bin/env bats

load helpers

function setup() {
	has_criu
	setup_test
}

function teardown() {
	cleanup_test
}

@test "checkpoint and restore one container into a new pod using --export" {
	CONTAINER_DROP_INFRA_CTR=false CONTAINER_ENABLE_CRIU_SUPPORT=true start_crio
	pod_id=$(crictl runp "$TESTDATA"/sandbox_config.json)
	ctr_id=$(crictl create "$pod_id" "$TESTDATA"/container_sleep.json "$TESTDATA"/sandbox_config.json)
	crictl start "$ctr_id"
	crictl checkpoint --export="$TESTDIR"/cp.tar "$ctr_id"
	crictl rmp -f "$pod_id"
	pod_id=$(crictl runp "$TESTDATA"/sandbox_config.json)
	# Replace original container with checkpoint image
	jq ".image.image=\"$TESTDIR/cp.tar\"" "$TESTDATA"/container_sleep.json > "$TESTDATA"/restore.json
	ctr_id=$(crictl create "$pod_id" "$TESTDATA"/restore.json "$TESTDATA"/sandbox_config.json)
	rm -f "$TESTDATA"/restore.json
	crictl rmp -f "$pod_id"
}

@test "checkpoint and restore one container into a new pod using --export to OCI image" {
	CONTAINER_DROP_INFRA_CTR=false CONTAINER_ENABLE_CRIU_SUPPORT=true start_crio
	pod_id=$(crictl runp "$TESTDATA"/sandbox_config.json)
	ctr_id=$(crictl create "$pod_id" "$TESTDATA"/container_sleep.json "$TESTDATA"/sandbox_config.json)
	crictl start "$ctr_id"
	crictl checkpoint --export="localhost/checkpoint-image:tag1" "$ctr_id"
	crictl rmp -f "$pod_id"
	pod_id=$(crictl runp "$TESTDATA"/sandbox_config.json)
	# Replace original container with checkpoint image
	jq ".image.image=\"localhost/checkpoint-image:tag1\"" "$TESTDATA"/container_sleep.json > "$TESTDATA"/restore.json
	ctr_id=$(crictl create "$pod_id" "$TESTDATA"/restore.json "$TESTDATA"/sandbox_config.json)
	rm -f "$TESTDATA"/restore.json
	crictl rmp -f "$pod_id"
}

@test "checkpoint and restore one container into a new pod using --export to OCI image using repoDigest" {
	CONTAINER_DROP_INFRA_CTR=false CONTAINER_ENABLE_CRIU_SUPPORT=true start_crio
	pod_id=$(crictl runp "$TESTDATA"/sandbox_config.json)
	ctr_id=$(crictl create "$pod_id" "$TESTDATA"/container_sleep.json "$TESTDATA"/sandbox_config.json)
	crictl start "$ctr_id"
	crictl checkpoint --export="localhost/checkpoint-image:tag1" "$ctr_id"
	# Kubernetes uses the repoDigest to references images.
	repo_digest=$(crictl inspecti --output go-template --template "{{(index .status.repoDigests 0)}}" "localhost/checkpoint-image:tag1")
	crictl rmp -f "$pod_id"
	pod_id=$(crictl runp "$TESTDATA"/sandbox_config.json)
	# Replace original container with checkpoint image
	jq ".image.image=\"$repo_digest\"" "$TESTDATA"/container_sleep.json > "$TESTDATA"/restore.json
	ctr_id=$(crictl create "$pod_id" "$TESTDATA"/restore.json "$TESTDATA"/sandbox_config.json)
	rm -f "$TESTDATA"/restore.json
	crictl rmp -f "$pod_id"
}
