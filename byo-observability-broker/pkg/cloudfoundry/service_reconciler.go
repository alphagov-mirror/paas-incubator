package cloudfoundry

import (
	"context"
	"fmt"
	"time"

	"code.cloudfoundry.org/cli/api/cloudcontroller/ccv2"
	"code.cloudfoundry.org/cli/api/cloudcontroller/ccv2/constant"
	"github.com/sirupsen/logrus"
)

type ServiceReconciler struct {
	OwnerLabel string
	Session    *Session
	Log        *logrus.Logger
	SpaceGUID  string
}

func (r *ServiceReconciler) Reconcile(ctx context.Context, service Service) error {
	serviceInstances, _, err := r.Session.ClientV2.GetServiceInstances(ccv2.Filter{
		Type:     "space_guid",
		Operator: ":",
		Values:   []string{r.SpaceGUID},
	}, ccv2.Filter{
		Type:     "name",
		Operator: ":",
		Values:   []string{service.InstanceName},
	})
	if err != nil {
		return err
	}
	if len(serviceInstances) < 1 {
		// find service by name
		services, _, err := r.Session.ClientV2.GetServices(ccv2.Filter{
			Type:     "label",
			Operator: ":",
			Values:   []string{service.ServiceName},
		})
		if err != nil {
			return err
		}
		if len(services) != 1 {
			return fmt.Errorf("failed to find service by label %s", service.ServiceName)
		}
		// find plan by name
		servicePlans, _, err := r.Session.ClientV2.GetServicePlans(ccv2.Filter{
			Type:     "service_guid",
			Operator: ":",
			Values:   []string{services[0].GUID},
		})
		if err != nil {
			return err
		}
		var servicePlan *ccv2.ServicePlan
		for i, plan := range servicePlans {
			if plan.Name == service.PlanName {
				servicePlan = &servicePlans[i]
			}
		}
		if servicePlan == nil {
			return fmt.Errorf("failed to find service plan by name: %s", service.PlanName)
		}
		// create service
		serviceInstance, _, err := r.Session.ClientV2.CreateServiceInstance(r.SpaceGUID, servicePlan.GUID, service.InstanceName, nil, nil)
		if err != nil {
			return err
		}
		// wait til ready
		if err := r.waitForState(ctx, serviceInstance.GUID, constant.LastOperationSucceeded); err != nil {
			return err
		}
	} else if len(serviceInstances) > 1 {
		return fmt.Errorf("got more service instances than expected")
	}
	return nil
}

func (r *ServiceReconciler) waitForState(ctx context.Context, serviceInstanceGUID string, targetState constant.LastOperationState) error {
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("context canceled")
		case <-time.After(2 * time.Second):
			serviceInstance, _, err := r.Session.ClientV2.GetServiceInstance(serviceInstanceGUID)
			if err != nil {
				return err
			}
			r.Log.Info("wait-for-state", targetState, "service", serviceInstanceGUID, "state", serviceInstance.LastOperation)
			if serviceInstance.LastOperation.State == targetState {
				return nil
			}
		}
	}
}
