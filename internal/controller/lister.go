/*
Copyright 2026 Dmitrii Zhukov.
Licensed under the Apache License, Version 2.0.
*/

package controller

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	monitoringv1alpha1 "github.com/dmazhukov/cronguard/api/v1alpha1"
)

// CachedLister implements metrics.Lister by listing CronJobMonitor objects
// from a controller-runtime client's cache.
type CachedLister struct {
	Client client.Client
}

// List returns all CronJobMonitor objects currently in the cache.
// An error during listing returns an empty slice (metrics should never fail scrape).
func (l *CachedLister) List() []monitoringv1alpha1.CronJobMonitor {
	var list monitoringv1alpha1.CronJobMonitorList
	if err := l.Client.List(context.Background(), &list); err != nil {
		return nil
	}
	return list.Items
}
