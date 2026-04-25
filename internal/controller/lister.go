/*
Copyright 2026 Dmitrii Zhukov.
Licensed under the Apache License, Version 2.0.
*/

package controller

import (
	"context"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	monitoringv1alpha1 "github.com/dmazhukov/cronguard/api/v1alpha1"
)

// CachedLister implements metrics.Lister by listing CronJobMonitor objects
// from a controller-runtime client's cache.
type CachedLister struct {
	Client client.Client
	Log    logr.Logger
}

// List returns all CronJobMonitor objects currently in the cache.
// On list error returns an empty slice — metrics scrape must never fail —
// but logs the error at V(1) so operators can see if the cache breaks.
func (l *CachedLister) List() []monitoringv1alpha1.CronJobMonitor {
	var list monitoringv1alpha1.CronJobMonitorList
	if err := l.Client.List(context.Background(), &list); err != nil {
		l.Log.V(1).Info("CachedLister: failed to list CronJobMonitors for metrics", "error", err.Error())
		return nil
	}
	return list.Items
}
