package reloader

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"sync"
	"syscall"
	"time"

	"code.cloudfoundry.org/cli/api/cloudcontroller/ccv2"
	"code.cloudfoundry.org/cli/api/cloudcontroller/ccv3"
	"github.com/alphagov/paas-incubator/byo-observability-broker/pkg/cloudfoundry"
	"github.com/mitchellh/go-ps"
	prommodel "github.com/prometheus/common/model"
	promconfig "github.com/prometheus/prometheus/config"
	promsd "github.com/prometheus/prometheus/discovery/config"
	promdns "github.com/prometheus/prometheus/discovery/dns"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/sirupsen/logrus"
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
	Log                    *logrus.Logger
	oldYAML                []byte
	sync.Mutex
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
			r.Log.Infof("skipping %s as it has no route mappings", app.Name)
			continue
		}
		// for each route, add dns service discovery config for suitable routes
		dnsConfigs := []*promdns.SDConfig{}
		for _, mapping := range routeMappings {
			route, _, err := r.Session.ClientV2.GetRoute(mapping.RouteGUID)
			if err != nil {
				return nil, err
			}
			domain, _, err := r.Session.ClientV2.GetSharedDomain(route.DomainGUID)
			if err != nil {
				return nil, err
			}
			scrapeAddr := fmt.Sprintf("%s.%s", route.Host, domain.Name)
			// Skip any non-internal routes to avoid auth issues
			// TODO: allow non-internal if given explicitly in binding params
			if !domain.Internal {
				r.Log.Warnf("skipping %s for %s as it is not an internal route", scrapeAddr, app.Name) // TODO: this is a a debug msg, normal behaviour
				continue
			}
			// since the ccv3/ccv2 libraries don't return port in route/destinations
			// we have to do this, otherwise there is no way to know what port the web process
			// is listening on
			// FIXME: remove this and use the value in "route.Destinations" once upstream
			// implement it.
			destinations, _, err := r.hackGetRouteDestinations(mapping.RouteGUID)
			if err != nil {
				return nil, err
			}
			for _, dest := range destinations {
				r.Log.Infof("considering %s:%d for %s...", scrapeAddr, dest.Port, app.Name) // TODO: this is a a debug msg, normal behaviour
				if dest.App.Process.Type != "web" {                                         // FIXME: is this always true? how else can we work out which port we mean
					r.Log.Infof("skipping %s:%d for %s as it is not a 'web' process route", scrapeAddr, dest.Port, app.Name) // TODO: this is a a debug msg, normal behaviour
					continue
				}
				dnsConfigs = append(dnsConfigs, &promdns.SDConfig{
					Names:           []string{scrapeAddr}, // TODO: get from binding params, fallback to this
					RefreshInterval: prommodel.Duration(30 * time.Second),
					Type:            "A",
					Port:            dest.Port, // TODO: get from binding params, fallback to this
				})
				r.Log.Infof("adding %s:%d as dns target for %s", scrapeAddr, dest.Port, app.Name) // TODO: this is a a debug msg, normal behaviour
			}
		}
		if len(dnsConfigs) < 1 {
			r.Log.Warnf("skipping %s as doesn't have any internal routes mapped", app.Name)
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
	var interval time.Duration
	for {
		if bytes.Equal(r.oldYAML, []byte{}) { // while no config
			interval = 2 * time.Second
		} else {
			interval = r.PollingInterval
		}
		reload := time.After(interval)
		r.Log.Infof("next reload at %v", time.Now().Add(r.PollingInterval))
		select {
		case <-reload:
			if err := r.updateConfig(ctx); err != nil {
				r.Log.Error(err)
				time.Sleep(30 * time.Second)
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func (r *Reloader) updateConfig(ctx context.Context) error {
	r.Lock()
	defer r.Unlock()
	// create config
	cfg, err := r.GenerateConfig()
	if err != nil {
		return err
	}
	cfgYAML, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if bytes.Equal(cfgYAML, []byte{}) {
		return fmt.Errorf("something went wrong, empty config generated")
	}
	if bytes.Equal(cfgYAML, r.oldYAML) {
		r.Log.Debug("config unchanged")
		return nil
	}
	// write the config to disk
	r.Log.Info("writing config:", r.TargetConfigPath)
	r.Log.Info(string(cfgYAML))
	if err := ioutil.WriteFile(r.TargetConfigPath, cfgYAML, 0644); err != nil {
		return err
	}
	// poke prometheus process to reload config
	if err := signalProcess("prometheus", syscall.SIGHUP); err != nil {
		return err
	}
	r.oldYAML = cfgYAML
	return nil
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

// I need the port in the destination, but ccv2 and ccv3 clients haven't implemnented it
// so make a func that fetches the full response
type RouteDestination struct {
	Port int
	ccv3.RouteDestination
}

func (r *Reloader) hackGetRouteDestinations(routeGUID string) ([]RouteDestination, []string, error) {
	var responseBody struct {
		Destinations []RouteDestination `json:"destinations"`
	}

	_, _, err := r.Session.ClientV3.MakeRequest(ccv3.RequestParams{
		RequestName:  "GetRouteDestinations",
		URIParams:    map[string]string{"route_guid": routeGUID},
		ResponseBody: &responseBody,
	})

	return responseBody.Destinations, nil, err
}
