package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alphagov/paas-incubator/byo-observability-broker/pkg/cloudfoundry"
	prommodel "github.com/prometheus/common/model"
	promconfig "github.com/prometheus/prometheus/config"
	promsd "github.com/prometheus/prometheus/discovery/config"
	promdns "github.com/prometheus/prometheus/discovery/dns"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

func MustGetEnv(key string) string {
	value := os.Getenv(key)
	if value == "" {
		log.Fatalln(fmt.Errorf("missing required environment variable %s", key))
	}
	return value
}

func NewCFConfigFromEnv() cloudfoundry.Config {
	config := cloudfoundry.Config{
		Endpoint:  MustGetEnv("CF_API_ENDPOINT"),
		Username:  MustGetEnv("CF_USERNAME"),
		Password:  MustGetEnv("CF_PASSWORD"),
		OrgName:   MustGetEnv("CF_ORG_NAME"),
		SpaceName: MustGetEnv("CF_SPACE_NAME"),
	}
	return config
}

func Main(ctx context.Context) error {
	// create a cf client
	cf, err := cloudfoundry.NewSession(NewCFConfigFromEnv())
	if err != nil {
		return err
	}
	log := logrus.New()
	appReconciler := cloudfoundry.ApplicationReconciler{
		Session: cf,
		Log:     log,
	}
	serviceReconciler := cloudfoundry.ServiceReconciler{
		Session:   cf,
		Log:       log,
		SpaceGUID: MustGetEnv("CF_SPACE_GUID"),
	}
	influx := cloudfoundry.Service{
		ServiceName:  "influxdb",
		InstanceName: "byo-test-influx",
		PlanName:     "tiny-1.x",
	}
	if err := serviceReconciler.Reconcile(ctx, influx); err != nil {
		return err
	}
	prometheusExporter := cloudfoundry.Application{
		Name: "test-prom-exporter",
		Metadata: &cloudfoundry.Metadata{
			Labels: map[string]string{
				"prometheus": "byo-test-prom",
			},
		},
		Memory:          "256M",
		DiskQuota:       "1G",
		Instances:       1,
		HealthCheckType: "port",
		Routes: []cloudfoundry.Route{
			{
				Route: "byo-test-prom-exporter.apps.internal",
			},
		},
		Buildpacks: []string{
			"binary_buildpack",
		},
		Path: filepath.Join("apps", "paas-prometheus-exporter"),
		Env: map[string]string{
			"USERNAME":         MustGetEnv("CF_USERNAME"),
			"PASSWORD":         MustGetEnv("CF_PASSWORD"),
			"API_ENDPOINT":     MustGetEnv("CF_API_ENDPOINT"),
			"UPDATE_FREQUENCY": "300",
			"SCRAPE_INTERVAL":  "60",
		},
	}
	additionalScrapeConfigs := []promconfig.ScrapeConfig{
		{
			JobName: "paas-exporter",
			Scheme:  "http",
			ServiceDiscoveryConfig: promsd.ServiceDiscoveryConfig{
				DNSSDConfigs: []*promdns.SDConfig{
					{
						Names:           []string{prometheusExporter.Routes[0].Route}, // TODO: get from binding params, fallback to this
						RefreshInterval: prommodel.Duration(30 * time.Second),
						Type:            "A",
						Port:            8080, // TODO: get from export's route somehow?
					},
				},
			},
		},
	}
	additionalScrapeConfigsYAML, err := yaml.Marshal(additionalScrapeConfigs)
	if err != nil {
		return err
	}
	prometheus := cloudfoundry.Application{
		Name: "test-prom",
		Metadata: &cloudfoundry.Metadata{
			Labels: map[string]string{
				"prometheus": "byo-test-prom",
			},
		},
		Memory:          "512M",
		DiskQuota:       "4G",
		Instances:       1,
		HealthCheckType: "port",
		Routes: []cloudfoundry.Route{
			{
				Route: "byo-test-prom.apps.internal",
			},
		},
		Buildpacks: []string{
			"https://github.com/alphagov/prometheus-buildpack.git",
		},
		Path: filepath.Join("apps", "prometheus"),
		Env: map[string]string{
			"PROMETHEUS_FLAGS": strings.Join([]string{
				"--storage.tsdb.retention.size=3GB",
				"--web.external-url=http://localhost",
				"--config.file=config.yml",
			}, " "),
			"CF_USERNAME":                         MustGetEnv("CF_USERNAME"),
			"CF_PASSWORD":                         MustGetEnv("CF_PASSWORD"),
			"CF_ORG_NAME":                         MustGetEnv("CF_ORG_NAME"),
			"CF_SPACE_NAME":                       MustGetEnv("CF_SPACE_NAME"),
			"CF_API_ENDPOINT":                     MustGetEnv("CF_API_ENDPOINT"),
			"CF_PROMETHEUS_SERVICE_INSTANCE_GUID": "10400cc3-9bf9-4d6d-ad16-46096febfff8", // FIXME: this is not right
			"PROMETHEUS_SCRAPE_CONFIGS":           string(additionalScrapeConfigsYAML),
		},
		Services: []string{
			influx.InstanceName,
		},
		Sidecars: []cloudfoundry.Sidecar{
			{
				Name:         "config-reloader",
				ProcessTypes: []string{"web"},
				Command:      "./prometheus-config-reloader",
			},
		},
	}
	grafanaAdminUser := "admin"
	grafanaAdminPassword := "password"
	grafanaPort := "3000"
	grafana := cloudfoundry.Application{
		Name: "test-grafana",
		Metadata: &cloudfoundry.Metadata{
			Labels: map[string]string{
				"prometheus": "byo-test-prom", // FIXME: should be parent service id
			},
		},
		Memory:          "256M",
		DiskQuota:       "1G",
		Instances:       1,
		HealthCheckType: "port",
		Routes: []cloudfoundry.Route{
			{
				Route: "byo-test-grafana.apps.internal",
			},
		},
		Path: filepath.Join("apps", "grafana"),
		Env: map[string]string{
			"GF_SECURITY_ADMIN_USER":     grafanaAdminUser,
			"GF_SECURITY_ADMIN_PASSWORD": grafanaAdminPassword,
			"GF_SERVER_HTTP_PORT":        grafanaPort, // FIXME: dynamic port?, it's 3000 cos docker image has an EXPOSE directive
		},
		Docker: &cloudfoundry.DockerConfig{
			Image: "grafana/grafana:7.0.1",
		},
		Command: `/run.sh`,
		Services: []string{
			influx.InstanceName,
		},
	}
	grafanaConfigReloader := cloudfoundry.Application{
		Name: "test-grafana-reloader",
		Metadata: &cloudfoundry.Metadata{
			Labels: map[string]string{
				"prometheus": "byo-test-prom", // FIXME: should be parent service id
			},
		},
		Buildpacks: []string{
			"binary_buildpack",
		},
		Command:         "./grafana-config-reloader",
		Memory:          "128M",
		DiskQuota:       "1G",
		Instances:       1,
		NoRoute:         true,
		HealthCheckType: "process",
		Path:            filepath.Join("apps", "grafana"),
		Env: map[string]string{
			"CF_USERNAME":     MustGetEnv("CF_USERNAME"),
			"CF_PASSWORD":     MustGetEnv("CF_PASSWORD"),
			"CF_API_ENDPOINT": MustGetEnv("CF_API_ENDPOINT"),
			"CF_ORG_NAME":     MustGetEnv("CF_ORG_NAME"),
			"CF_SPACE_NAME":   MustGetEnv("CF_SPACE_NAME"),
			"GF_API_ENDPOINT": fmt.Sprintf("http://%s:%s",
				grafana.Routes[0].Route, // FIXME: do better
				grafanaPort,
			),
			"GF_API_KEY": fmt.Sprintf("%s:%s", grafanaAdminUser, grafanaAdminPassword),
			"PROMETHEUS_URL": fmt.Sprintf("http://%s:%d",
				prometheus.Routes[0].Route, // FIXME: do better
				8080,                       // FIXME: this can change
			),
		},
		Services: []string{
			influx.InstanceName,
		},
	}
	manifest := cloudfoundry.Manifest{
		Applications: []cloudfoundry.Application{
			prometheus,
			prometheusExporter,
			grafana,
			grafanaConfigReloader,
		},
	}
	if err := appReconciler.Reconcile(ctx, manifest); err != nil {
		return err
	}
	// add a network policy to let grafanaConfigReloader talk to grafana
	_ = cf.CLIClient.AddNetworkPolicy(
		grafanaConfigReloader.Name,
		grafana.Name,
		grafanaPort, // FIXME: not 8080 like the others due to docker EXPOSE
	)
	// add a network policy to let grafana talk to prometheus
	_ = cf.CLIClient.AddNetworkPolicy(
		grafana.Name,
		prometheus.Name,
		"8080",
	)
	// add a network policy to let prometheus talk to paas-exporter
	// TODO: move to the prom config reloader?
	_ = cf.CLIClient.AddNetworkPolicy(
		prometheus.Name,
		prometheusExporter.Name,
		"8080",
	)
	// add a network policy to let prometheus scrape grafana
	// TODO: move to the prom config reloader?
	_ = cf.CLIClient.AddNetworkPolicy(
		prometheus.Name,
		grafana.Name,
		grafanaPort,
	)
	return nil
}

func main() {
	ctx := context.Background()
	if err := Main(ctx); err != nil {
		log.Fatal(err)
	}
}
