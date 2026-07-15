package proxy

import (
	"os"
	"strings"
)

// BuildVersion is replaced by -ldflags during release/container builds.
// Runtime environment values win so Railway source deployments can expose the
// exact commit even when their Docker builder does not pass custom build args.
var BuildVersion = "dev"

// CurrentBuildVersion returns the most specific version available for the
// running process. APP_VERSION is an explicit operator override;
// RAILWAY_GIT_COMMIT_SHA is supplied by Railway for repository deployments.
func CurrentBuildVersion() string {
	for _, candidate := range []string{
		os.Getenv("APP_VERSION"),
		os.Getenv("RAILWAY_GIT_COMMIT_SHA"),
		BuildVersion,
	} {
		if version := strings.TrimSpace(candidate); version != "" {
			return version
		}
	}
	return "dev"
}
