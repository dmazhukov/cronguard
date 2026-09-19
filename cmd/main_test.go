/*
Copyright 2026 Dmitrii Zhukov.
Licensed under the Apache License, Version 2.0.
*/

package main

import "testing"

func TestMetricsOptions(t *testing.T) {
	for _, tc := range []struct {
		addr   string
		secure bool
	}{{":8080", false}, {":8443", true}} {
		got := metricsOptions(tc.addr, tc.secure)
		if got.BindAddress != tc.addr || got.SecureServing != tc.secure {
			t.Errorf("metricsOptions(%q, %v) = {BindAddress: %q, SecureServing: %v}", tc.addr, tc.secure, got.BindAddress, got.SecureServing)
		}
		if got.FilterProvider != nil {
			t.Errorf("metricsOptions(%q, %v) installs a filter; the operator carries no RBAC for one", tc.addr, tc.secure)
		}
	}
}
