/*

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

		http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"flag"

	"k8s.io/apimachinery/pkg/runtime"

	"os"

	"github.com/Azure/azure-service-operator/controllers"
	resourcemanagersqldb "github.com/Azure/azure-service-operator/pkg/resourcemanager/azuresql/azuresqldb"
	resourcemanagersqlfailovergroup "github.com/Azure/azure-service-operator/pkg/resourcemanager/azuresql/azuresqlfailovergroup"
	resourcemanagersqlfirewallrule "github.com/Azure/azure-service-operator/pkg/resourcemanager/azuresql/azuresqlfirewallrule"
	resourcemanagersqlserver "github.com/Azure/azure-service-operator/pkg/resourcemanager/azuresql/azuresqlserver"
	resourcemanagersqluser "github.com/Azure/azure-service-operator/pkg/resourcemanager/azuresql/azuresqluser"
	resourcemanagerconfig "github.com/Azure/azure-service-operator/pkg/resourcemanager/config"
	resourcemanagereventhub "github.com/Azure/azure-service-operator/pkg/resourcemanager/eventhubs"
	resourcemanagerkeyvault "github.com/Azure/azure-service-operator/pkg/resourcemanager/keyvaults"
	resourcemanagerresourcegroup "github.com/Azure/azure-service-operator/pkg/resourcemanager/resourcegroups"
	resourcemanagerstorage "github.com/Azure/azure-service-operator/pkg/resourcemanager/storages"
	"github.com/Azure/azure-service-operator/pkg/secrets"
	keyvaultSecrets "github.com/Azure/azure-service-operator/pkg/secrets/keyvault"
	k8sSecrets "github.com/Azure/azure-service-operator/pkg/secrets/kube"

	azurev1alpha1 "github.com/Azure/azure-service-operator/api/v1alpha1"
	telemetry "github.com/Azure/azure-service-operator/pkg/telemetry"
	kscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	// +kubebuilder:scaffold:imports
)

var (
	masterURL, kubeconfig, resources, clusterName               string
	cloudName, tenantID, subscriptionID, clientID, clientSecret string
	useAADPodIdentity                                           bool

	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {

	_ = kscheme.AddToScheme(scheme)
	_ = azurev1alpha1.AddToScheme(scheme)
	// +kubebuilder:scaffold:scheme
}

// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=azure.microsoft.com,resources=events,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=azure.microsoft.com,resources=azuresqlusers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments/status,verbs=get;update;patch

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var secretClient secrets.SecretClient
	flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "The address the metric endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.")

	flag.Parse()

	ctrl.SetLogger(zap.Logger(true))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:             scheme,
		MetricsBindAddress: metricsAddr,
		LeaderElection:     enableLeaderElection,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	err = resourcemanagerconfig.ParseEnvironment()
	if err != nil {
		setupLog.Error(err, "unable to parse settings required to provision resources in Azure")
		os.Exit(1)
	}

	keyvaultName := resourcemanagerconfig.OperatorKeyvault()

	if keyvaultName == "" {
		setupLog.Info("Keyvault name is empty")
		secretClient = k8sSecrets.New(mgr.GetClient())
	} else {
		setupLog.Info("Instantiating secrets client for keyvault " + keyvaultName)
		secretClient = keyvaultSecrets.New(keyvaultName)
	}

	resourceGroupManager := resourcemanagerresourcegroup.NewAzureResourceGroupManager()
	eventhubManagers := resourcemanagereventhub.AzureEventHubManagers
	eventhubNamespaceClient := resourcemanagereventhub.NewEventHubNamespaceClient(ctrl.Log.WithName("controllers").WithName("EventhubNamespace"))
	consumerGroupClient := resourcemanagereventhub.NewConsumerGroupClient(ctrl.Log.WithName("controllers").WithName("ConsumerGroup"))
	storageManagers := resourcemanagerstorage.AzureStorageManagers
	keyVaultManager := resourcemanagerkeyvault.AzureKeyVaultManager
	eventhubClient := resourcemanagereventhub.NewEventhubClient(secretClient, scheme)
	sqlServerManager := resourcemanagersqlserver.NewAzureSqlServerManager(ctrl.Log.WithName("sqlservermanager").WithName("AzureSqlServer"))
	sqlDBManager := resourcemanagersqldb.NewAzureSqlDbManager(ctrl.Log.WithName("sqldbmanager").WithName("AzureSqlDb"))
	sqlFirewallRuleManager := resourcemanagersqlfirewallrule.NewAzureSqlFirewallRuleManager(ctrl.Log.WithName("sqlfirewallrulemanager").WithName("AzureSqlFirewallRule"))
	sqlFailoverGroupManager := resourcemanagersqlfailovergroup.NewAzureSqlFailoverGroupManager(ctrl.Log.WithName("sqlfailovergroupmanager").WithName("AzureSqlFailoverGroup"))
	sqlUserManager := resourcemanagersqluser.NewAzureSqlUserManager(ctrl.Log.WithName("sqlusermanager").WithName("AzureSqlUser"))

	err = (&controllers.StorageReconciler{
		Client:         mgr.GetClient(),
		Log:            ctrl.Log.WithName("controllers").WithName("Storage"),
		Recorder:       mgr.GetEventRecorderFor("Storage-controller"),
		Scheme:         mgr.GetScheme(),
		StorageManager: storageManagers.Storage,
	}).SetupWithManager(mgr)
	if err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Storage")
		os.Exit(1)
	}
	err = (&controllers.CosmosDBReconciler{
		Client:   mgr.GetClient(),
		Log:      ctrl.Log.WithName("controllers").WithName("CosmosDB"),
		Recorder: mgr.GetEventRecorderFor("CosmosDB-controller"),
	}).SetupWithManager(mgr)
	if err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "CosmosDB")
		os.Exit(1)
	}
	if err = (&controllers.RedisCacheReconciler{
		Client:   mgr.GetClient(),
		Log:      ctrl.Log.WithName("controllers").WithName("RedisCache"),
		Recorder: mgr.GetEventRecorderFor("RedisCache-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "RedisCache")
		os.Exit(1)
	}

	err = (&controllers.EventhubReconciler{
		Reconciler: &controllers.AsyncReconciler{
			Client:      mgr.GetClient(),
			AzureClient: eventhubClient,
			Telemetry: telemetry.InitializePrometheusDefault(
				ctrl.Log.WithName("controllers").WithName("Eventhub"),
				"Eventhub",
			),
			Recorder: mgr.GetEventRecorderFor("Eventhub-controller"),
			Scheme:   scheme,
		},
	}).SetupWithManager(mgr)
	if err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Eventhub")
		os.Exit(1)
	}
	err = (&controllers.ResourceGroupReconciler{
		Reconciler: &controllers.AsyncReconciler{
			Client:      mgr.GetClient(),
			AzureClient: resourceGroupManager,
			Telemetry: telemetry.InitializePrometheusDefault(
				ctrl.Log.WithName("controllers").WithName("ResourceGroup"),
				"ResourceGroup",
			),
			Recorder: mgr.GetEventRecorderFor("ResourceGroup-controller"),
			Scheme:   scheme,
		},
	}).SetupWithManager(mgr)
	if err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ResourceGroup")
		os.Exit(1)
	}

	err = (&controllers.EventhubNamespaceReconciler{
		Reconciler: &controllers.AsyncReconciler{
			Client:      mgr.GetClient(),
			AzureClient: eventhubNamespaceClient,
			Telemetry: telemetry.InitializePrometheusDefault(
				ctrl.Log.WithName("controllers").WithName("EventhubNamespace"),
				"EventhubNamespace",
			),
			Recorder: mgr.GetEventRecorderFor("EventhubNamespace-controller"),
			Scheme:   scheme,
		},
	}).SetupWithManager(mgr)
	if err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "EventhubNamespace")
		os.Exit(1)
	}

	if err = (&controllers.KeyVaultReconciler{
		Client:          mgr.GetClient(),
		Log:             ctrl.Log.WithName("controllers").WithName("KeyVault"),
		Recorder:        mgr.GetEventRecorderFor("KeyVault-controller"),
		KeyVaultManager: keyVaultManager,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "KeyVault")
		os.Exit(1)
	}

	err = (&controllers.ConsumerGroupReconciler{
		Client:               mgr.GetClient(),
		Log:                  ctrl.Log.WithName("controllers").WithName("ConsumerGroup"),
		Recorder:             mgr.GetEventRecorderFor("ConsumerGroup-controller"),
		ConsumerGroupManager: eventhubManagers.ConsumerGroup,
		Reconciler: &controllers.AsyncReconciler{
			Client:      mgr.GetClient(),
			AzureClient: consumerGroupClient,
			Telemetry: telemetry.InitializePrometheusDefault(
				ctrl.Log.WithName("controllers").WithName("ConsumerGroup"),
				"ConsumerGroup",
			),
			Recorder: mgr.GetEventRecorderFor("ConsumerGroup-controller"),
			Scheme:   scheme,
		},
	}).SetupWithManager(mgr)
	if err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ConsumerGroup")
		os.Exit(1)
	}
	if err = (&controllers.AzureSqlServerReconciler{
		Client:                mgr.GetClient(),
		Log:                   ctrl.Log.WithName("controllers").WithName("AzureSqlServer"),
		Recorder:              mgr.GetEventRecorderFor("AzureSqlServer-controller"),
		Scheme:                mgr.GetScheme(),
		AzureSqlServerManager: sqlServerManager,
		SecretClient:          secretClient,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "AzureSqlServer")
		os.Exit(1)
	}
	if err = (&controllers.AzureSqlDatabaseReconciler{
		Client:            mgr.GetClient(),
		Log:               ctrl.Log.WithName("controllers").WithName("AzureSqlDatabase"),
		Recorder:          mgr.GetEventRecorderFor("AzureSqlDatabase-controller"),
		Scheme:            mgr.GetScheme(),
		AzureSqlDbManager: sqlDBManager,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "AzureSqlDatabase")
		os.Exit(1)
	}
	if err = (&controllers.AzureSqlFirewallRuleReconciler{
		Client: mgr.GetClient(),
		Telemetry: telemetry.InitializePrometheusDefault(
			ctrl.Log.WithName("controllers").WithName("AzureSQLFirewallRuleOperator"),
			"AzureSQLFirewallRuleOperator",
		),
		Recorder:                    mgr.GetEventRecorderFor("SqlFirewallRule-controller"),
		Scheme:                      mgr.GetScheme(),
		AzureSqlFirewallRuleManager: sqlFirewallRuleManager,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SqlFirewallRule")
		os.Exit(1)
	}
	if err = (&controllers.AzureSqlActionReconciler{
		Client:                mgr.GetClient(),
		Log:                   ctrl.Log.WithName("controllers").WithName("AzureSqlAction"),
		Recorder:              mgr.GetEventRecorderFor("AzureSqlAction-controller"),
		Scheme:                mgr.GetScheme(),
		AzureSqlServerManager: sqlServerManager,
		SecretClient:          secretClient,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "AzureSqlAction")
		os.Exit(1)
	}
	if err = (&controllers.AzureSQLUserReconciler{
		Client:              mgr.GetClient(),
		Log:                 ctrl.Log.WithName("controllers").WithName("AzureSQLUser"),
		Recorder:            mgr.GetEventRecorderFor("AzureSQLUser-controller"),
		Scheme:              mgr.GetScheme(),
		AzureSqlUserManager: sqlUserManager,
		SecretClient:        secretClient,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "AzureSQLUser")
		os.Exit(1)
	}
	if err = (&controllers.AzureSqlFailoverGroupReconciler{
		Client:                       mgr.GetClient(),
		Log:                          ctrl.Log.WithName("controllers").WithName("AzureSqlFailoverGroup"),
		Recorder:                     mgr.GetEventRecorderFor("AzureSqlFailoverGroup-controller"),
		Scheme:                       mgr.GetScheme(),
		AzureSqlFailoverGroupManager: sqlFailoverGroupManager,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "AzureSqlFailoverGroup")
		os.Exit(1)
	}

	if err = (&controllers.BlobContainerReconciler{
		Client:         mgr.GetClient(),
		Log:            ctrl.Log.WithName("controllers").WithName("BlobContainer"),
		Recorder:       mgr.GetEventRecorderFor("BlobContainer-controller"),
		Scheme:         mgr.GetScheme(),
		StorageManager: storageManagers.BlobContainer,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "BlobContainer")
		os.Exit(1)
	}

	if err = (&controllers.AzureDataLakeGen2FileSystemReconciler{
		Client:            mgr.GetClient(),
		Log:               ctrl.Log.WithName("controllers").WithName("AzureDataLakeGen2FileSystem"),
		Recorder:          mgr.GetEventRecorderFor("AzureDataLakeGen2FileSystem-controller"),
		FileSystemManager: storageManagers.FileSystem,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "AzureDataLakeGen2FileSystem")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
