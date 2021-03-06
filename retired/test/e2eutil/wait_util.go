package e2eutil

import (
	goctx "context"
	"github.com/thelastpickle/reaper-operator/pkg/apis/reaper/v1alpha1"
	casskop "github.com/Orange-OpenSource/cassandra-k8s-operator/pkg/apis/db/v1alpha1"
	framework "github.com/operator-framework/operator-sdk/pkg/test"
	"github.com/operator-framework/operator-sdk/pkg/test/e2eutil"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"testing"
	"time"
)

type ReaperConditionFunc func(reaper *v1alpha1.Reaper) (bool, error)

// Waits for the CassandraCluster with namespace/name to be ready. Specifically, this
// function checks for CassandraCluster.State.Phase == Running.
func WaitForCassKopCluster(
	t *testing.T,
	f *framework.Framework,
	namespace string,
	name string,
	retryInterval time.Duration,
	timeout time.Duration,) error {

	return wait.Poll(retryInterval, timeout, func() (bool, error) {
		cc := &casskop.CassandraCluster{}
		err := f.Client.Get(goctx.TODO(), types.NamespacedName{Name: name, Namespace: namespace}, cc)
		if err != nil {
			if apierrors.IsNotFound(err) {
				t.Logf("Waiting for availability of CassandraCluster %s\n", name)
				return false, nil
			}
			return false, err
		}
		if cc.Status.Phase != "Running" {
			t.Logf("Waiting for CassandraCluster %s (%s)\n", name, cc.Status.Phase)
			return false, nil
		}
		return true, nil
	})
}

func WaitForOperatorDeployment(t *testing.T,
	f *framework.Framework,
	namespace string,
	name string,
	retryInterval time.Duration,
	timeout time.Duration,) error {

	return e2eutil.WaitForDeployment(t, f.KubeClient, namespace, name, 1, retryInterval, timeout)
}

func WaitForReaperToBeReady(t *testing.T,
	f *framework.Framework,
	namespace string,
	name string,
	retryInterval time.Duration,
	timeout time.Duration) error {

	return wait.Poll(retryInterval, timeout, func() (bool, error) {
		reaper := &v1alpha1.Reaper{}

		if err := f.Client.Get(goctx.TODO(), types.NamespacedName{Namespace: namespace, Name: name}, reaper); err!= nil {
			if apierrors.IsNotFound(err) {
				t.Logf("Reaper %s not found\n", name)
				return false, nil
			}
			return false, nil
		}
		if reaper.Status.ReadyReplicas == 0 {
			t.Logf("Waiting for Reaper %s\n", name)
			return false, nil
		}

		return true, nil
	})
}

func WaitForReaperCondition(t *testing.T,
	f *framework.Framework,
	namespace string,
	name string,
	retryInterval time.Duration,
	timeout time.Duration,
	condition ReaperConditionFunc) error {

	return wait.Poll(retryInterval, timeout, func() (done bool, err error) {
		reaper := &v1alpha1.Reaper{}

		if err := f.Client.Get(goctx.TODO(), types.NamespacedName{Namespace: namespace, Name: name}, reaper); err!= nil {
			if apierrors.IsNotFound(err) {
				t.Logf("Reaper %s not found\n", name)
				return false, nil
			}
			return false, nil
		}

		return condition(reaper)
	})
}

func WaitForDeploymentToBeDeleted(t *testing.T,
	f *framework.Framework,
	namespace string,
	name string,
	retryInteral time.Duration,
	timeout time.Duration) error {

	d := &appsv1.Deployment{}
	return waitForObjectToBeDeleted(t, f, d, namespace, name, retryInteral, timeout)
}

func WaitForConfigMapToBeDeleted(t *testing.T,
	f *framework.Framework,
	namespace string,
	name string,
	retryInteral time.Duration,
	timeout time.Duration) error {

	cm := &corev1.ConfigMap{}
	return waitForObjectToBeDeleted(t, f, cm, namespace, name, retryInteral, timeout)
}

func WaitForServiceToBeDeleted(t *testing.T,
	f *framework.Framework,
	namespace string,
	name string,
	retryInteral time.Duration,
	timeout time.Duration) error {

	svc := &corev1.Service{}
	return waitForObjectToBeDeleted(t, f, svc, namespace, name, retryInteral, timeout)
}

func waitForObjectToBeDeleted(t *testing.T,
	f *framework.Framework,
	obj runtime.Object,
	namespace string,
	name string,
	retryInteral time.Duration,
	timeout time.Duration) error {

	return wait.Poll(retryInteral, timeout, func() (bool, error) {
		err := f.Client.Get(goctx.TODO(), types.NamespacedName{Namespace: namespace, Name: name}, obj)

		if err == nil {
			return false, nil
		}

		if apierrors.IsNotFound(err) {
			return true, nil
		}

		t.Logf("Failed to get %s (%s): %s", obj.GetObjectKind().GroupVersionKind().Kind, name, err)
		return false, err
	})
}
