package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/util/wait"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/cloud-provider/app"
	"k8s.io/cloud-provider/app/config"
	"k8s.io/cloud-provider/names"
	"k8s.io/cloud-provider/options"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/version/verflag"
)

func withVersion(t *testing.T, value string) {
	t.Helper()
	previous := version
	version = value
	t.Cleanup(func() { version = previous })
}

func newRecordingCommand(t *testing.T, output *bytes.Buffer) (*cobra.Command, *bool) {
	t.Helper()
	hasRun := false
	command := &cobra.Command{
		Use: "test",
		RunE: func(*cobra.Command, []string) error {
			hasRun = true
			return nil
		},
	}
	verflag.AddFlags(command.Flags())
	command.SetOut(output)
	command.SetErr(output)
	reportVersion(command, output)
	t.Cleanup(func() {
		if resetError := command.Flags().Set("version", "false"); resetError != nil {
			t.Errorf("reset --version: %v", resetError)
		}
	})
	return command, &hasRun
}

func TestVersionFlagPrintsTheReleaseVersion(t *testing.T) {
	withVersion(t, "v0.1.0")
	var output bytes.Buffer
	command, hasRun := newRecordingCommand(t, &output)
	command.SetArgs([]string{"--version"})

	if executeError := command.Execute(); executeError != nil {
		t.Fatalf("execute: %v", executeError)
	}
	if got := output.String(); got != "ankra-cloud-ccm v0.1.0\n" {
		t.Fatalf("--version printed %q, want %q", got, "ankra-cloud-ccm v0.1.0\n")
	}
	if *hasRun {
		t.Fatal("--version started the controller manager")
	}
}

func TestRawVersionFlagPrintsTheReleaseVersionAndLibrary(t *testing.T) {
	withVersion(t, "v0.1.0")
	var output bytes.Buffer
	command, hasRun := newRecordingCommand(t, &output)
	command.SetArgs([]string{"--version=raw"})

	if executeError := command.Execute(); executeError != nil {
		t.Fatalf("execute: %v", executeError)
	}
	got := output.String()
	for _, want := range []string{`Version:"v0.1.0"`, "CloudProviderLibrary:", "GoVersion:"} {
		if !strings.Contains(got, want) {
			t.Fatalf("--version=raw printed %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "$Format") {
		t.Fatalf("--version=raw printed the component-base placeholder: %q", got)
	}
	if *hasRun {
		t.Fatal("--version=raw started the controller manager")
	}
}

func TestWithoutVersionFlagTheCommandRuns(t *testing.T) {
	withVersion(t, "v0.1.0")
	var output bytes.Buffer
	command, hasRun := newRecordingCommand(t, &output)
	command.SetArgs([]string{})

	if executeError := command.Execute(); executeError != nil {
		t.Fatalf("execute: %v", executeError)
	}
	if !*hasRun {
		t.Fatal("the command did not run without --version")
	}
	if output.Len() != 0 {
		t.Fatalf("printed %q without --version", output.String())
	}
}

func TestCommandWithoutVersionFlagIsLeftToRun(t *testing.T) {
	hasRun := false
	command := &cobra.Command{
		Use: "test",
		RunE: func(*cobra.Command, []string) error {
			hasRun = true
			return nil
		},
	}
	var output bytes.Buffer
	reportVersion(command, &output)
	command.SetArgs([]string{})

	if executeError := command.Execute(); executeError != nil {
		t.Fatalf("execute: %v", executeError)
	}
	if !hasRun || output.Len() != 0 {
		t.Fatalf("hasRun %v, output %q", hasRun, output.String())
	}
}

func TestCloudControllerManagerCommandReportsTheReleaseVersion(t *testing.T) {
	withVersion(t, "v0.1.0")
	controllerManagerOptions, optionsError := options.NewCloudControllerManagerOptions()
	if optionsError != nil {
		t.Fatalf("options: %v", optionsError)
	}
	initialiser := func(*config.CompletedConfig) cloudprovider.Interface {
		t.Fatal("--version initialised the cloud provider")
		return nil
	}
	command := app.NewCloudControllerManagerCommand(controllerManagerOptions, initialiser, app.DefaultInitFuncConstructors,
		names.CCMControllerAliases(), cliflag.NamedFlagSets{}, wait.NeverStop)
	var output bytes.Buffer
	reportVersion(command, &output)
	command.SetArgs([]string{"--version"})
	t.Cleanup(func() {
		if resetError := command.Flags().Set("version", "false"); resetError != nil {
			t.Errorf("reset --version: %v", resetError)
		}
	})

	if executeError := command.Execute(); executeError != nil {
		t.Fatalf("execute: %v", executeError)
	}
	if got := output.String(); got != "ankra-cloud-ccm v0.1.0\n" {
		t.Fatalf("--version printed %q, want %q", got, "ankra-cloud-ccm v0.1.0\n")
	}
}
