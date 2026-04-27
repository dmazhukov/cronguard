/*
Copyright 2026 Dmitrii Zhukov.
Licensed under the Apache License, Version 2.0.
*/

package controller

import (
	"context"
	"time"

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

// List returns the cached CronJobMonitor objects, or nil on cache failure.
func (l *CachedLister) List() []monitoringv1alpha1.CronJobMonitor {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var list monitoringv1alpha1.CronJobMonitorList
	if err := l.Client.List(ctx, &list); err != nil {
		l.Log.V(1).Info("CachedLister: list failed", "error", err.Error())
		return nil
	}
	return list.Items
}
