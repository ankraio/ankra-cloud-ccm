package main

import (
	"fmt"
	"io"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

const programName = "ankra-cloud-ccm"

// versionInfo is what --version=raw prints: the release this binary was built as, and the Kubernetes cloud-provider
// library it embeds.
type versionInfo struct {
	Version              string
	CloudProviderLibrary string
	GoVersion            string
	Platform             string
}

// reportVersion makes --version and --version=raw print the release version set with -X main.version. The
// cloud-provider command prints k8s.io/component-base/version instead, which this build leaves at its
// v0.0.0-master placeholder on purpose: that package's version also sets the Kubernetes binary version that
// feature gates and --show-hidden-metrics-for-version are checked against, so it must not become v0.1.0.
// --version=vX.Y.Z still reaches the cloud-provider command, which uses it to override the reported version.
func reportVersion(command *cobra.Command, output io.Writer) {
	run := command.RunE
	command.RunE = func(command *cobra.Command, arguments []string) error {
		versionFlag := command.Flags().Lookup("version")
		if versionFlag == nil {
			return run(command, arguments)
		}
		switch versionFlag.Value.String() {
		case "true":
			_, writeError := fmt.Fprintf(output, "%s %s\n", programName, version)
			return writeError
		case "raw":
			_, writeError := fmt.Fprintf(output, "%#v\n", currentVersionInfo())
			return writeError
		default:
			return run(command, arguments)
		}
	}
}

func currentVersionInfo() versionInfo {
	return versionInfo{
		Version:              version,
		CloudProviderLibrary: moduleVersion("k8s.io/cloud-provider"),
		GoVersion:            runtime.Version(),
		Platform:             runtime.GOOS + "/" + runtime.GOARCH,
	}
}

func moduleVersion(path string) string {
	buildInfo, isAvailable := debug.ReadBuildInfo()
	if !isAvailable {
		return "unknown"
	}
	for _, dependency := range buildInfo.Deps {
		if dependency.Path == path {
			return dependency.Version
		}
	}
	return "unknown"
}
