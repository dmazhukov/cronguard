package leader_test

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/dmazhukov/cronguard/internal/leader"
)

func TestSetRoleAddsLabel(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cronguard-abc",
			Namespace: "cronguard-system",
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()

	l := &leader.Labeler{
		Client:       cl,
		PodName:      "cronguard-abc",
		PodNamespace: "cronguard-system",
	}
	if err := l.SetRole(context.Background(), leader.RoleLeader); err != nil {
		t.Fatalf("SetRole: %v", err)
	}

	got := &corev1.Pod{}
	if err := cl.Get(context.Background(),
		types.NamespacedName{Name: "cronguard-abc", Namespace: "cronguard-system"},
		got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Labels[leader.RoleLabel] != leader.RoleLeader {
		t.Fatalf("Labels[%q] = %q, want %q",
			leader.RoleLabel, got.Labels[leader.RoleLabel], leader.RoleLeader)
	}
}

func TestSetRoleNoOpWithoutIdentity(t *testing.T) {
	l := &leader.Labeler{}
	if err := l.SetRole(context.Background(), leader.RoleLeader); err != nil {
		t.Fatalf("SetRole on empty labeler should not error: %v", err)
	}
}
