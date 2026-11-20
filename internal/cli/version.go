package cli

// version is the functional version of the binary. It changes with the
// contract, not with the build.
const version = "0.4.0"

// Build metadata, injected by the release build and left at its defaults
// otherwise:
//
//	go build -ldflags "-X dbvfs/internal/cli.commit=$(git rev-parse --short HEAD) -X dbvfs/internal/cli.buildState=release"
var (
	commit     = "unknown"
	buildState = "dev"
)

// versionData is the `version` command shape (DESIGN §10).
type versionData struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	State   string `json:"state"`
}

func versionInfo() versionData {
	return versionData{Version: version, Commit: commit, State: buildState}
}
