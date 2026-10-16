// Package buildinfo names the running build. Release images set both values
// at link time:
//
//	go build -ldflags "-X github.com/dappnode/dappnode-nexus-gateway/pkg/buildinfo.Version=v0.1.67 -X github.com/dappnode/dappnode-nexus-gateway/pkg/buildinfo.Commit=<sha>"
package buildinfo

var (
	// Version is the release tag, or "dev" for local builds.
	Version = "dev"
	// Commit is the source revision, when known.
	Commit = ""
)
