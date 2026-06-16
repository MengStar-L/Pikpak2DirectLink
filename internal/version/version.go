// Package version exposes the build version of the server. The value is stamped
// at build time so the self-updater can compare the running binary against the
// latest GitHub release.
package version

// Version is the running build version. It is overridden at build time via:
//
//	go build -ldflags "-X pikpak2directlink/internal/version.Version=v1.2.3" ./cmd/server
//
// Local/dev builds leave it as "dev"; the updater treats "dev" as "older than
// any published release" so a dev build can still pull a real release.
var Version = "dev"

// DefaultRepo is the GitHub "owner/name" the updater checks for new releases.
// It can be overridden at runtime via the UPDATE_REPO environment variable.
const DefaultRepo = "MengStar-L/Pikpak2DirectLink"
