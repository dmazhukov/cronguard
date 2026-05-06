// Package leader provides a label-updater that flips the operator pod's
// `cronguard.io/role` label between "leader" and "standby" based on
// controller-runtime's leader-election lifecycle. Only the leader pod
// should be scraped for metrics in HA installs (otherwise Prometheus
// double-counts every cronguard_* gauge).
package leader

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// RoleLabel is the pod-metadata label CronGuard owns.
const RoleLabel = "cronguard.io/role"

// Role values written to RoleLabel.
const (
	RoleLeader  = "leader"
	RoleStandby = "standby"
)

// Labeler patches its own pod's label to reflect leader-election state.
type Labeler struct {
	Client       client.Client
	PodName      string
	PodNamespace string
}

// SetRole patches the pod's RoleLabel using a strategic merge patch.
// Idempotent: re-applying the same role is a no-op on the apiserver.
func (l *Labeler) SetRole(ctx context.Context, role string) error {
	if l.PodName == "" || l.PodNamespace == "" {
		log.FromContext(ctx).V(1).Info("labeler: pod identity unset, skipping",
			"pod", l.PodName, "namespace", l.PodNamespace)
		return nil
	}
	patch := map[string]any{
		"metadata": map[string]any{
			"labels": map[string]any{
				RoleLabel: role,
			},
		},
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshal label patch: %w", err)
	}
	pod := &corev1.Pod{}
	pod.SetName(l.PodName)
	pod.SetNamespace(l.PodNamespace)
	if err := l.Client.Patch(ctx, pod, client.RawPatch(types.StrategicMergePatchType, body)); err != nil {
		return fmt.Errorf("patch pod %s/%s: %w", l.PodNamespace, l.PodName, err)
	}
	return nil
}
