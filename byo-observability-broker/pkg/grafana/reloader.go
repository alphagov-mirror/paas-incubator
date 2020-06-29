package grafana

import (
	"context"
	"log"
	"time"

	"github.com/alphagov/paas-incubator/byo-observability-broker/pkg/cloudfoundry"
	cfenv "github.com/cloudfoundry-community/go-cfenv"
	grafanasdk "github.com/grafana-tools/sdk"
	"github.com/sirupsen/logrus"
)

var BasicAuth = true

type Reloader struct {
	Session         *cloudfoundry.Session
	PollingInterval time.Duration
	Log             *logrus.Logger
	Grafana         *grafanasdk.Client
	Bindings        map[string][]cfenv.Service
	PrometheusURL   string
}

func (r *Reloader) Run(ctx context.Context) error {
	for {
		log.Printf("next reload at %v", time.Now().Add(r.PollingInterval))
		select {
		case <-r.Reload():
			if err := r.updateConfigs(ctx); err != nil {
				r.Log.Error(err)
				time.Sleep(30 * time.Second)
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func (r *Reloader) Reload() <-chan time.Time {
	return time.After(r.PollingInterval)
}

func (r *Reloader) updateConfigs(ctx context.Context) error {
	desiredDatasources := []grafanasdk.Datasource{}
	for serviceName, bindings := range r.Bindings {
		switch serviceName {
		case "influxdb":
			for _, binding := range bindings {
				// add influx datasource
				defaultdb := "defaultdb"
				ds := grafanasdk.Datasource{
					ID:        uint(len(desiredDatasources) + 1), // FIXME: this is prob not deterministic
					OrgID:     1,                                 // FIXME: ??
					Type:      "influxdb",
					Access:    "proxy", // TODO: guessed?
					Name:      binding.Name,
					BasicAuth: &BasicAuth,
					Database:  &defaultdb,
				}
				for k, v := range binding.Credentials {
					switch k {
					case "uri":
						ds.URL, _ = v.(string)
					case "username":
						s, _ := v.(string)
						ds.BasicAuthUser = &s
						ds.User = &s
					case "password":
						s, _ := v.(string)
						ds.BasicAuthPassword = &s
						ds.Password = &s
					}
				}
				desiredDatasources = append(desiredDatasources, ds)
			}
		default:
			r.Log.Debugf("ignoring binding of %s service")
			continue
		}
	}
	// add prometheus
	// TODO: if prometheus was a real service, then it could use the method above and be less weird
	if r.PrometheusURL != "" {
		prom := grafanasdk.Datasource{
			ID:        uint(len(desiredDatasources) + 1), // FIXME: this is prob not deterministic
			OrgID:     1,                                 // FIXME: guess
			Type:      "prometheus",
			Access:    "proxy", // TODO: guessed?
			Name:      "prometheus-0",
			URL:       r.PrometheusURL,
			IsDefault: true,
		}
		desiredDatasources = append(desiredDatasources, prom)
	}
	// sync datasources
	for _, ds := range desiredDatasources {
		if err := r.findOrCreateDatasource(ctx, ds); err != nil {
			return err
		}
	}

	// write dashboards to /etc/grafana/provisioning/dashboards/
	return nil
}

func (r *Reloader) findOrCreateDatasource(ctx context.Context, desiredDS grafanasdk.Datasource) error {
	existingDatasources, err := r.Grafana.GetAllDatasources(ctx)
	if err != nil {
		return err
	}
	if existingDS, ok := findDatasource(existingDatasources, desiredDS.Name); ok { // update
		desiredDS.ID = existingDS.ID
		_, err := r.Grafana.UpdateDatasource(ctx, desiredDS)
		if err != nil {
			return err
		}
	} else { // create
		_, err := r.Grafana.CreateDatasource(ctx, desiredDS)
		if err != nil {
			return err
		}
	}
	return nil
}

func findDatasource(list []grafanasdk.Datasource, name string) (*grafanasdk.Datasource, bool) {
	for _, ds := range list {
		if ds.Name == name {
			return &ds, true
		}
	}
	return nil, false
}
