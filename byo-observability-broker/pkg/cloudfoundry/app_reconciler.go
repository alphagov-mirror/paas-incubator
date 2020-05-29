package cloudfoundry

import (
	"context"

	"github.com/sirupsen/logrus"
)

type ApplicationReconciler struct {
	OwnerLabel string
	Session    *Session
	Log        *logrus.Logger
}

func (r *ApplicationReconciler) Reconcile(ctx context.Context, manifest Manifest) error {
	if err := r.Session.CLIClient.Push(manifest); err != nil {
		return err
	}
	return nil
}
