// Package version holds the one build version both binaries report:
// zfs-endpoint in GetCapabilities and -version, zfs-csi in GetPluginInfo
// and -version.
//
// A release build sets it with the linker:
//
//	go build -ldflags "-X github.com/wommy/empty/internal/version.Version=v0.1.0" ./cmd/...
package version

// Version is "dev" unless set by -ldflags -X (see the package comment).
var Version = "dev"
