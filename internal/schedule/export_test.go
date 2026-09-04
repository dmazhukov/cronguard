/*
Copyright 2026 Dmitrii Zhukov.
Licensed under the Apache License, Version 2.0.
*/

package schedule

import "github.com/robfig/cron/v3"

// NewForTest builds a Schedule around an arbitrary cron.Schedule. It exists so
// tests can instrument the underlying oracle — e.g. to assert that Prev's work
// stays bounded — without exporting the field itself.
func NewForTest(expr cron.Schedule) *Schedule {
	return &Schedule{expr: expr}
}
