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
	s, err := cloudfoundry.NewSession(NewCFConfigFromEnv())
	if err != nil {
		return err
	}
	log := logrus.New()
	appReconciler := cloudfoundry.ApplicationReconciler{
		Session: s,
		Log:     log,
	}
	serviceReconciler := cloudfoundry.ServiceReconciler{
		Session:   s,
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
	prometheusExporter := cloudfoundry.Manifest{
		Applications: []cloudfoundry.Application{
			{
				Name: "test-prom-exporter",
				Metadata: &cloudfoundry.Metadata{
					Labels: map[string]string{
						"prometheus": "byo-test-prom",
					},
				},
				Memory:    "256M",
				DiskQuota: "1G",
				Instances: 1,
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
			},
		},
	}
	if err := appReconciler.Reconcile(ctx, prometheusExporter); err != nil {
		return err
	}
	additionalScrapeConfigs := []promconfig.ScrapeConfig{
		{
			JobName: "paas-exporter",
			Scheme:  "http",
			ServiceDiscoveryConfig: promsd.ServiceDiscoveryConfig{
				DNSSDConfigs: []*promdns.SDConfig{
					{
						Names:           []string{prometheusExporter.Applications[0].Routes[0].Route}, // TODO: get from binding params, fallback to this
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
	prometheus := cloudfoundry.Manifest{
		Applications: []cloudfoundry.Application{
			{
				Name: "test-prom",
				Metadata: &cloudfoundry.Metadata{
					Labels: map[string]string{
						"prometheus": "byo-test-prom",
					},
				},
				Memory:    "512M",
				DiskQuota: "4G",
				Instances: 1,
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
			},
		},
	}
	if err := appReconciler.Reconcile(ctx, prometheus); err != nil {
		return err
	}
	grafana := cloudfoundry.Manifest{
		Applications: []cloudfoundry.Application{
			{
				Name: "test-grafana",
				Metadata: &cloudfoundry.Metadata{
					Labels: map[string]string{
						"prometheus": "byo-test-prom", // FIXME: should be parent service id
					},
				},
				Memory:    "256M",
				DiskQuota: "1G",
				Instances: 1,
				Routes: []cloudfoundry.Route{
					{
						Route: "byo-test-grafana.apps.internal",
					},
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
					"CF_API_ENDPOINT":                     MustGetEnv("CF_API_ENDPOINT"),
					"CF_PROMETHEUS_SERVICE_INSTANCE_GUID": "10400cc3-9bf9-4d6d-ad16-46096febfff8", // FIXME: this is not right
					"PROMETHEUS_SCRAPE_CONFIGS":           string(additionalScrapeConfigsYAML),
					"GF_AUTH_GOOGLE_CLIENT_ID":            "",
					"GF_AUTH_GOOGLE_CLIENT_SECRET":        "",
					"GF_SERVER_HTTP_PORT":                 "",
				},
				Docker: cloudfoundry.DockerConfig{
					Image: "6.7.3",
				},
				Command: `/run.sh`,
				Sidecars: []cloudfoundry.Sidecar{
					{
						Name:         "config-reloader",
						ProcessTypes: []string{"web"},
						Command:      "./grafana-config-reloader",
					},
				},
			},
		},
	}
	if err := appReconciler.Reconcile(ctx, grafana); err != nil {
		return err
	}
	return nil
}

func main() {
	ctx := context.Background()
	if err := Main(ctx); err != nil {
		log.Fatal(err)
	}
}
