//go:build tools
// +build tools

package tools

import (
	_ "github.com/cpuguy83/go-md2man"
	_ "github.com/golang/mock/mockgen"
	_ "github.com/onsi/ginkgo/ginkgo"
	_ "github.com/psampaz/go-mod-outdated"
	_ "google.golang.org/grpc/cmd/protoc-gen-go-grpc"
	_ "google.golang.org/protobuf/cmd/protoc-gen-go"
	_ "k8s.io/release/cmd/release-notes"
	_ "mvdan.cc/sh/v3/cmd/shfmt"
	_ "sigs.k8s.io/zeitgeist"
)
