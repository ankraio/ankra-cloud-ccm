// Command ankra-cloud-ccm is the Kubernetes cloud controller manager for Ankra Cloud: the k8s.io/cloud-provider
// command with the ankracloud provider registered. Run it with --cloud-provider=ankracloud and the kubelets with
// --cloud-provider=external.
package main

import (
	"os"

	"k8s.io/apimachinery/pkg/util/wait"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/cloud-provider/app"
	"k8s.io/cloud-provider/app/config"
	"k8s.io/cloud-provider/names"
	"k8s.io/cloud-provider/options"
	"k8s.io/component-base/cli"
	cliflag "k8s.io/component-base/cli/flag"
	_ "k8s.io/component-base/logs/json/register"
	_ "k8s.io/component-base/metrics/prometheus/clientgo"
	_ "k8s.io/component-base/metrics/prometheus/version"
	"k8s.io/klog/v2"

	"github.com/ankraio/ankra-cloud-ccm/internal/provider"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	provider.Register(os.LookupEnv, "ankra-cloud-ccm/"+version)
	controllerManagerOptions, optionsError := options.NewCloudControllerManagerOptions()
	if optionsError != nil {
		klog.Fatalf("initialise the command options: %v", optionsError)
	}
	command := app.NewCloudControllerManagerCommand(controllerManagerOptions, initialiseCloud, app.DefaultInitFuncConstructors,
		names.CCMControllerAliases(), cliflag.NamedFlagSets{}, wait.NeverStop)
	reportVersion(command, os.Stdout)
	os.Exit(cli.Run(command))
}

func initialiseCloud(completedConfiguration *config.CompletedConfig) cloudprovider.Interface {
	cloudConfiguration := completedConfiguration.ComponentConfig.KubeCloudShared.CloudProvider
	if cloudConfiguration.Name != provider.ProviderName {
		klog.Fatalf("--cloud-provider must be %s, not %q", provider.ProviderName, cloudConfiguration.Name)
	}
	cloud, initialiseError := cloudprovider.InitCloudProvider(cloudConfiguration.Name, cloudConfiguration.CloudConfigFile)
	if initialiseError != nil {
		klog.Fatalf("initialise the %s cloud provider: %v", provider.ProviderName, initialiseError)
	}
	if cloud == nil {
		klog.Fatalf("the %s cloud provider is not registered", provider.ProviderName)
	}
	return cloud
}
