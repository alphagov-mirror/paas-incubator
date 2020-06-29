package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/alphagov/paas-incubator/byo-observability-broker/pkg/cloudfoundry"
	"github.com/alphagov/paas-incubator/byo-observability-broker/pkg/grafana"
	cfenv "github.com/cloudfoundry-community/go-cfenv"
	grafanasdk "github.com/grafana-tools/sdk"
	"github.com/sirupsen/logrus"
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
	// create grafana client
	gf := grafanasdk.NewClient(
		MustGetEnv("GF_API_ENDPOINT"),
		MustGetEnv("GF_API_KEY"),
		grafanasdk.DefaultHTTPClient,
	)
	// fetch info from VCAP_APPLICATION
	appEnv, err := cfenv.Current()
	if err != nil {
		return err
	}
	// create a reloader
	reloader := grafana.Reloader{
		PollingInterval: 60 * time.Second,
		Session:         cf,
		Grafana:         gf,
		Log:             logrus.New(),
		Bindings:        appEnv.Services,
		PrometheusURL:   MustGetEnv("PROMETHEUS_URL"),
	}
	return reloader.Run(ctx)
}

func main() {
	ctx := context.Background()
	if err := Main(ctx); err != nil {
		log.Fatal(err)
	}
}
