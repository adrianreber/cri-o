#!/usr/bin/env bats

load helpers

function setup() {
	has_criu
	setup_test
}

function teardown() {
	cleanup_test
}

@test "checkpoint and restore one container into original pod" {
	CONTAINER_DROP_INFRA_CTR=false CONTAINER_ENABLE_CRIU_SUPPORT=true start_crio
	pod_id=$(crictl runp "$TESTDATA"/sandbox_config.json)
	ctr_id=$(crictl create "$pod_id" "$TESTDATA"/container_redis.json "$TESTDATA"/sandbox_config.json)
	crictl start "$ctr_id"
	crioctl checkpoint "$ctr_id"
	crioctl restore "$ctr_id"
	crictl rmp -f "$pod_id"
}

@test "checkpoint and restore one container into a new pod" {
	CONTAINER_DROP_INFRA_CTR=false CONTAINER_ENABLE_CRIU_SUPPORT=true start_crio
	pod_id=$(crictl runp "$TESTDATA"/sandbox_config.json)
	ctr_id=$(crictl create "$pod_id" "$TESTDATA"/container_redis.json "$TESTDATA"/sandbox_config.json)
	crictl start "$ctr_id"
	crioctl checkpoint "$ctr_id"
	new_pod_id=$(crictl runp "$TESTDATA"/sandbox_config_restore.json)
	crioctl restore -p "$new_pod_id" "$ctr_id"
	crictl rmp -f "$new_pod_id"
	crictl rmp -f "$pod_id"
}

@test "checkpoint and restore one container into a new pod using --export" {
	CONTAINER_DROP_INFRA_CTR=false CONTAINER_ENABLE_CRIU_SUPPORT=true start_crio
	pod_id=$(crictl runp "$TESTDATA"/sandbox_config.json)
	ctr_id=$(crictl create "$pod_id" "$TESTDATA"/container_redis.json "$TESTDATA"/sandbox_config.json)
	crictl start "$ctr_id"
	crioctl checkpoint --export="$TESTDIR"/cp.tar "$ctr_id"
	crictl rmp -f "$pod_id"
	pod_id=$(crictl runp "$TESTDATA"/sandbox_config.json)
	crioctl restore -p "$pod_id" --import="$TESTDIR"/cp.tar
	crictl rmp -f "$pod_id"
}

@test "checkpoint and restore one pod using --export" {
	CONTAINER_ENABLE_CRIU_SUPPORT=true start_crio
	pod_id=$(crictl runp "$TESTDATA"/sandbox_config_restore.json)
	ctr_id=$(crictl create "$pod_id" "$TESTDATA"/container_redis.json "$TESTDATA"/sandbox_config_restore.json)
	ctr_id_sleep=$(crictl create "$pod_id" "$TESTDATA"/container_sleep.json "$TESTDATA"/sandbox_config_restore.json)
	crictl start "$ctr_id"
	crictl start "$ctr_id_sleep"
	crioctl checkpoint --export="$TESTDIR"/cp.tar "$pod_id"
	crictl rmp -f "$pod_id"
	pod_id=$(crioctl restore --import="$TESTDIR"/cp.tar | jq -r '.[0].id')
	crictl rmp -f "$pod_id"
}
