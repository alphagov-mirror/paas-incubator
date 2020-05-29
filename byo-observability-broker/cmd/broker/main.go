package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"code.cloudfoundry.org/lager"
	"github.com/alphagov/paas-incubator/byo-observability-broker/pkg/observability/provider"
	"github.com/alphagov/paas-service-broker-base/broker"
	"github.com/pivotal-cf/brokerapi/domain"
	"github.com/pivotal-cf/brokerapi/domain/apiresponses"
)

func MustGetEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		panic(fmt.Sprintf("Environment variable %s is required", k))
	}
	return v
}

func run(ctx context.Context) error {
	config := broker.Config{
		API: broker.API{
			BasicAuthUsername: MustGetEnv("BROKER_USERNAME"),
			BasicAuthPassword: MustGetEnv("BROKER_PASSWORD"),
			LogLevel:          "DEBUG",
		},
		Catalog: broker.Catalog{
			Catalog: apiresponses.CatalogResponse{
				Services: []domain.Service{
					{
						ID:            "723f0986-eab1-4739-822c-8d7c2ed620a5",
						Name:          "prometheus",
						Description:   "influxdb backed prometheus metrics collection",
						Bindable:      true,
						PlanUpdatable: false,
						// Requires: []domain.RequiredPermission{
						// 	domain.PermissionSyslogDrain,
						// },
						Metadata: &domain.ServiceMetadata{
							DisplayName:         "prometheus",
							ImageUrl:            "https://en.wikipedia.org/wiki/Prometheus_(software)#/media/File:Prometheus_software_logo.svg",
							LongDescription:     "self managed prometheus instance backed by influxdb",
							ProviderDisplayName: "BYO",
							DocumentationUrl:    "",
							SupportUrl:          "",
						},
						Plans: []domain.ServicePlan{
							{
								ID:          "1514ef4d-09d0-4dd1-93ab-8bbf770e3b15",
								Name:        "tiny",
								Description: "prometheus 2.x backed by Aiven InfluxDB tiny-1.x",
								Metadata: &domain.ServicePlanMetadata{
									DisplayName: "tiny",
								},
							},
						},
					},
				},
			},
		},
	}

	logger := lager.NewLogger("observability-broker")
	logger.RegisterSink(lager.NewWriterSink(os.Stdout, config.API.LagerLogLevel))

	observabilityProvider, err := provider.New("<THIS_IS_NOT_A_GUID_SILLY>")
	if err != nil {
		return fmt.Errorf("Error creating provider: %v\n", err)
	}

	go func(ctx context.Context) {
		for {
			if err := observabilityProvider.Reconcile(ctx); err != nil {
				logger.Error("reconcile", err)
			}
			time.Sleep(60 * time.Second)
		}
	}(ctx)

	observabilityBroker, err := broker.New(config, observabilityProvider, logger)
	if err != nil {
		return fmt.Errorf("Error creating service broker: %s", err)
	}

	brokerAPI := broker.NewAPI(observabilityBroker, logger, config)

	fmt.Println("service broker started on port " + config.API.Port + "...")
	return http.ListenAndServe(":3000", brokerAPI)
}

func main() {
	ctx := context.Background()
	if err := run(ctx); err != nil {
		log.Fatal(err)
	}
}
