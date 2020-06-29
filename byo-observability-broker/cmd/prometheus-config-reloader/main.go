package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"strings"
	"time"

	"github.com/alphagov/paas-incubator/byo-observability-broker/pkg/cloudfoundry"
	"github.com/alphagov/paas-incubator/byo-observability-broker/pkg/prometheus/reloader"
	cfenv "github.com/cloudfoundry-community/go-cfenv"
	config_util "github.com/prometheus/common/config"
	promconfig "github.com/prometheus/prometheus/config"
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
	// find endpint from VCAP_APPLICATION
	appEnv, err := cfenv.Current()
	if err != nil {
		panic(err)
	}
	token := os.Getenv("CF_TOKEN") // mainly for local dev
	if token != "" {
		return cloudfoundry.Config{
			Endpoint: appEnv.CFAPI,
			Token:    strings.Replace(token, "bearer ", "", 1),
		}
	}
	return cloudfoundry.Config{
		Endpoint: appEnv.CFAPI,
		Username: MustGetEnv("CF_USERNAME"),
		Password: MustGetEnv("CF_PASSWORD"),
	}
}

func Main(ctx context.Context) error {
	// create a cf client
	cf, err := cloudfoundry.NewSession(NewCFConfigFromEnv())
	if err != nil {
		return err
	}
	// fetch info from VCAP_APPLICATION
	appEnv, err := cfenv.Current()
	if err != nil {
		return err
	}
	// create a reloader
	reloader := reloader.Reloader{
		SourceConfigPath:       "default-prometheus-config.yml",
		TargetConfigPath:       "config.yml",
		PrometheusInstanceGUID: MustGetEnv("CF_PROMETHEUS_SERVICE_INSTANCE_GUID"),
		PollingInterval:        60 * time.Second,
		Session:                cf,
		Labels: map[string]string{
			"space": appEnv.SpaceName,
		},
		Log: logrus.New(),
	}
	// check for any scrape configs passed from environment
	additionalScrapeConfigsYAML := os.Getenv("PROMETHEUS_SCRAPE_CONFIGS")
	if additionalScrapeConfigsYAML != "" {
		additionalScrapeConfigs := []*promconfig.ScrapeConfig{}
		err := yaml.Unmarshal([]byte(additionalScrapeConfigsYAML), &additionalScrapeConfigs)
		if err != nil {
			return err
		}
		reloader.ScrapeConfigs = additionalScrapeConfigs
	}
	// detect if we have influx backing and add relevent config
	// TODO: move this into reloader
	influxServices, ok := appEnv.Services["influxdb"]
	if ok {
		for _, influxService := range influxServices {
			creds, ok := influxService.Credentials["prometheus"]
			if !ok {
				return fmt.Errorf("expected to find a 'prometheus' section in the influxdb VCAP_SERVICES, but didn't")
			}
			// parse vcap -> promconfig
			var remoteConfigs struct {
				RemoteRead  []*promconfig.RemoteReadConfig  `yaml:"remote_read"`
				RemoteWrite []*promconfig.RemoteWriteConfig `yaml:"remote_write"`
			}
			remoteConfigBytes, err := yaml.Marshal(creds)
			if err != nil {
				return err
			}
			if err := yaml.Unmarshal(remoteConfigBytes, &remoteConfigs); err != nil {
				return err
			}
			// the promconfig package does some weird stuff redacting secrets on yaml.Marshal
			// so to avoid the secret litrally become the string "<secret>" we'll write it to a file
			// and rewrite the config... which is a real shame
			for _, c := range remoteConfigs.RemoteRead {
				cancel, err := fixClientConfigSecret(&c.HTTPClientConfig)
				defer cancel()
				if err != nil {
					return err
				}
			}
			// .. more yuk now for the RemoteWrite...
			for _, c := range remoteConfigs.RemoteWrite {
				cancel, err := fixClientConfigSecret(&c.HTTPClientConfig)
				defer cancel()
				if err != nil {
					return err
				}
			}
			// add to reloader config
			reloader.RemoteReadConfigs = append(reloader.RemoteReadConfigs, remoteConfigs.RemoteRead...)
			reloader.RemoteWriteConfigs = append(reloader.RemoteWriteConfigs, remoteConfigs.RemoteWrite...)
		}
	}

	return reloader.Run(ctx)
}

// hack up a basicauth block to avoid the yaml marshaller redacting our secret
// returns cancel func which should be called to tidy up tmpfiles
func fixClientConfigSecret(c *config_util.HTTPClientConfig) (func(), error) {
	fn := func() {}
	pass := c.BasicAuth.Password
	tmpfile, err := ioutil.TempFile("", "pass")
	if err != nil {
		return fn, err
	}
	fn = func() { os.Remove(tmpfile.Name()) }
	if _, err := tmpfile.Write([]byte(pass)); err != nil {
		return fn, err
	}
	if err := tmpfile.Close(); err != nil {
		return fn, err
	}
	c.BasicAuth.Password = ""
	c.BasicAuth.PasswordFile = tmpfile.Name()
	return fn, nil
}

func main() {
	ctx := context.Background()
	if err := Main(ctx); err != nil {
		log.Fatal(err)
	}
}
