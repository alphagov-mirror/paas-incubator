package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/alphagov/paas-incubator/byo-observability-broker/pkg/prometheus"
	provideriface "github.com/alphagov/paas-service-broker-base/provider"
	"github.com/pivotal-cf/brokerapi"
	"github.com/pivotal-cf/brokerapi/domain"
)

var (
	ErrUpdateNotSupported = errors.New("Updating service is currently not supported")
)

type Provider struct {
	Owner string
}

func (p *Provider) Provision(ctx context.Context, provisionData provideriface.ProvisionData) (dashboardURL, operationData string, isAsync bool, err error) {
	// no-op or maybe provision an index based on provisionData.Details.ServiceID
	return "", "", false, err
}

func (p *Provider) Deprovision(ctx context.Context, deprovisionData provideriface.DeprovisionData) (operationData string, isAsync bool, err error) {
	// no-op or maybe delete an index? (probably don't really want to do that!)
	return "", false, err
}

func (p *Provider) Bind(ctx context.Context, bindData provideriface.BindData) (binding domain.Binding, err error) {
	return domain.Binding{
		IsAsync: false,
	}, nil
}

func (p *Provider) Unbind(ctx context.Context, unbindData provideriface.UnbindData) (domain.UnbindSpec, error) {
	// no op
	return domain.UnbindSpec{
		IsAsync: false,
	}, nil
}

func (p *Provider) Update(ctx context.Context, updateData provideriface.UpdateData) (
	operationData string, isAsync bool, err error) {
	return "", false, ErrUpdateNotSupported
}

func (p *Provider) LastOperation(ctx context.Context, lastOperationData provideriface.LastOperationData) (state domain.LastOperationState, description string, err error) {
	return brokerapi.Succeeded, "Last operation polling not required. All operations are synchronous.", nil
}

func (p *Provider) Reconcile(ctx context.Context) error {
	var errs []error
	prometheusReconciler := prometheus.Reconciler{
		OwnerLabel: p.Owner,
	}
	if err := prometheusReconciler.Reconcile(ctx); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return fmt.Errorf("reconcile: %v", errs)
	}
	return nil
}

func New(ownerGUID string) (*Provider, error) {
	return &Provider{
		Owner: ownerGUID,
	}, nil
}
