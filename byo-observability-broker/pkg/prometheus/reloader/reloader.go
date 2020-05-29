package reloader

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"syscall"
	"time"

	"code.cloudfoundry.org/cli/api/cloudcontroller/ccv2"
	"github.com/alphagov/paas-incubator/byo-observability-broker/pkg/cloudfoundry"
	"github.com/mitchellh/go-ps"
	prommodel "github.com/prometheus/common/model"
	promconfig "github.com/prometheus/prometheus/config"
	promsd "github.com/prometheus/prometheus/discovery/config"
	promdns "github.com/prometheus/prometheus/discovery/dns"
	"github.com/prometheus/prometheus/pkg/labels"
	yaml "gopkg.in/yaml.v2"
)

type Reloader struct {
	SourceConfigPath       string
	TargetConfigPath       string
	PollingInterval        time.Duration
	Session                *cloudfoundry.Session
	PrometheusInstanceGUID string
	Labels                 map[string]string
	RemoteWriteConfigs     []*promconfig.RemoteWriteConfig
	RemoteReadConfigs      []*promconfig.RemoteReadConfig
	ScrapeConfigs          []*promconfig.ScrapeConfig
}

func (r *Reloader) GenerateConfig() (*promconfig.Config, error) {
	// load the base config
	cfg, err := promconfig.LoadFile(r.SourceConfigPath)
	if err != nil {
		return nil, err
	}
	// discover targets based on bindings
	scrapeConfigs, err := r.generateScrapeConfigsForBindings()
	if err != nil {
		return nil, err
	}
	// append discovery to the base config
	cfg.ScrapeConfigs = append(cfg.ScrapeConfigs, scrapeConfigs...)
	// append any configs from the reloader config
	cfg.ScrapeConfigs = append(cfg.ScrapeConfigs, r.ScrapeConfigs...)
	// append any labels
	for k, v := range r.Labels {
		cfg.GlobalConfig.ExternalLabels = append(cfg.GlobalConfig.ExternalLabels, labels.Label{
			Name:  k,
			Value: v,
		})
	}
	// add any remote read/write configs (ie for influxdb backing)
	cfg.RemoteReadConfigs = append(cfg.RemoteReadConfigs, r.RemoteReadConfigs...)
	cfg.RemoteWriteConfigs = append(cfg.RemoteWriteConfigs, r.RemoteWriteConfigs...)

	return cfg, nil
}

func (r *Reloader) generateScrapeConfigsForBindings() ([]*promconfig.ScrapeConfig, error) {
	// fetch all service bindings of prom service instance
	bindings, _, err := r.Session.ClientV2.GetServiceBindings(ccv2.Filter{
		Type:     "service_instance_guid",
		Operator: ":",
		Values:   []string{r.PrometheusInstanceGUID},
	})
	if err != nil {
		return nil, err
	}
	// for each binding, create DNS sd config by looking at routes/
	scrapeConfigs := []*promconfig.ScrapeConfig{}
	for _, binding := range bindings {
		app, _, err := r.Session.ClientV2.GetApplication(binding.AppGUID)
		if err != nil {
			return nil, err
		}
		// fetch the route mappings for this app
		routeMappings, _, err := r.Session.ClientV2.GetRouteMappings(ccv2.Filter{
			Type:     "app_guid",
			Operator: ":",
			Values:   []string{app.GUID},
		})
		if len(routeMappings) < 1 {
			log.Printf("skipping %s as it has no route mappings", app.Name)
			continue
		}
		// for each route, add dns service discovery config for suitable routes
		dnsConfigs := []*promdns.SDConfig{}
		for _, mapping := range routeMappings {
			route, _, err := r.Session.ClientV2.GetRoute(mapping.RouteGUID)
			if err != nil {
				return nil, err
			}
			port := 8080
			if route.Port.IsSet {
				port = route.Port.Value
			}
			domain, _, err := r.Session.ClientV2.GetSharedDomain(route.DomainGUID)
			if err != nil {
				return nil, err
			}
			scrapeAddr := fmt.Sprintf("%s.%s", route.Host, domain.Name)
			// Skip any non-internal routes to avoid auth issues
			// TODO: allow non-internal if given explicitly in binding params
			if !domain.Internal {
				log.Printf("skipping %s for %s as it is not an internal route", scrapeAddr, app.Name) // TODO: this is a a debug msg, normal behaviour
				continue
			}
			dnsConfigs = append(dnsConfigs, &promdns.SDConfig{
				Names:           []string{scrapeAddr}, // TODO: get from binding params, fallback to this
				RefreshInterval: prommodel.Duration(30 * time.Second),
				Type:            "A",
				Port:            port, // TODO: get from binding params, fallback to this
			})
			log.Printf("adding %s as dns target for %s", scrapeAddr, app.Name) // TODO: this is a a debug msg, normal behaviour
		}
		if len(dnsConfigs) < 1 {
			log.Printf("skipping %s as doesn't have any internal routes mapped", app.Name)
			continue
		}
		scrapeConfigs = append(scrapeConfigs, &promconfig.ScrapeConfig{
			JobName:        app.Name,
			ScrapeInterval: prommodel.Duration(30 * time.Second),
			ServiceDiscoveryConfig: promsd.ServiceDiscoveryConfig{
				DNSSDConfigs: dnsConfigs,
			},
			MetricsPath: "/metrics", // TODO: get from binding params, fallback to this
		})
	}
	return scrapeConfigs, nil
}

func (r *Reloader) Run(ctx context.Context) error {
	var oldYAML []byte
	var interval time.Duration
	for {
		if bytes.Equal(oldYAML, []byte{}) { // while no config
			interval = 2 * time.Second
		} else {
			interval = r.PollingInterval
		}
		reload := time.After(interval)
		log.Printf("next reload at %v", time.Now().Add(r.PollingInterval))
		select {
		case <-reload:
			// create config
			cfg, err := r.GenerateConfig()
			if err != nil {
				return err // TODO: maybe not go bang here
			}
			cfgYAML, err := yaml.Marshal(cfg)
			if err != nil {
				return err
			}
			if bytes.Equal(cfgYAML, []byte{}) {
				return fmt.Errorf("something went wrong, empty config generated")
			}
			if bytes.Equal(cfgYAML, oldYAML) {
				log.Println("config unchanged")
				continue
			}
			// write the config to disk
			log.Println("writing config:", r.TargetConfigPath)
			log.Println(string(cfgYAML))
			if err := ioutil.WriteFile(r.TargetConfigPath, cfgYAML, 0644); err != nil {
				return err
			}
			// poke prometheus process to reload config
			if err := signalProcess("prometheus", syscall.SIGHUP); err != nil {
				log.Println(err)
				continue
			}
			// record config to avoid reloads when no changes
			oldYAML = cfgYAML
		case <-ctx.Done():
			return nil
		}
	}
}

func signalProcess(name string, sig syscall.Signal) error {
	procs, err := ps.Processes()
	if err != nil {
		return err
	}
	for _, proc := range procs {
		if proc.Executable() == name {
			log.Printf("sending %s to %s process %d\n", sig, proc.Executable(), proc.Pid())
			if err := syscall.Kill(proc.Pid(), sig); err != nil {
				return err
			}
			log.Println("config reloaded")
		}
	}
	return nil
}
