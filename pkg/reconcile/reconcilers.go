package reconcile

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	api "github.com/thelastpickle/reaper-operator/api/v1alpha1"
	"github.com/thelastpickle/reaper-operator/pkg/config"
	mlabels "github.com/thelastpickle/reaper-operator/pkg/labels"
	"github.com/thelastpickle/reaper-operator/pkg/status"
	appsv1 "k8s.io/api/apps/v1"
	v1batch "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	schemaJobImage           = "jsanda/create_keyspace:latest"
	schemaJobImagePullPolicy = corev1.PullIfNotPresent
)

// ReaperRequest containers the information necessary to perform reconciliation actions on a Reaper object.
type ReaperRequest struct {
	Reaper *api.Reaper

	Logger logr.Logger

	StatusManager *status.StatusManager
}

type ServiceReconciler interface {
	ReconcileService(ctx context.Context, req ReaperRequest) (*ctrl.Result, error)
}

type SchemaReconciler interface {
	ReconcileSchema(ctx context.Context, req ReaperRequest) (*ctrl.Result, error)
}

type DeploymentReconciler interface {
	ReconcileDeployment(ctx context.Context, req ReaperRequest) (*ctrl.Result, error)
}

type defaultReconciler struct {
	client.Client

	scheme *runtime.Scheme

	secretsManager SecretsManager
}

var reconciler defaultReconciler

func InitReconcilers(client client.Client, scheme *runtime.Scheme) {
	reconciler = defaultReconciler{
		Client:         client,
		scheme:         scheme,
		secretsManager: NewSecretsManager(),
	}
}

func GetServiceReconciler() ServiceReconciler {
	return &reconciler
}

func GetSchemaReconciler() SchemaReconciler {
	return &reconciler
}

func GetDeploymentReconciler() DeploymentReconciler {
	return &reconciler
}

func (r *defaultReconciler) ReconcileService(ctx context.Context, req ReaperRequest) (*ctrl.Result, error) {
	reaper := req.Reaper
	key := types.NamespacedName{Namespace: reaper.Namespace, Name: GetServiceName(reaper.Name)}

	req.Logger.Info("reconciling service", "service", key)

	service := &corev1.Service{}
	err := r.Client.Get(ctx, key, service)
	if err != nil && errors.IsNotFound(err) {
		// create the service
		service = newService(key, reaper)
		if err = controllerutil.SetControllerReference(reaper, service, r.scheme); err != nil {
			req.Logger.Error(err, "failed to set owner reference on service", "service", key)
			return &ctrl.Result{Requeue: true, RequeueAfter: 10 * time.Second}, err
		}

		req.Logger.Info("creating service", "service", key)
		if err = r.Client.Create(ctx, service); err != nil {
			req.Logger.Error(err, "failed to create service", "service", key)
			return &ctrl.Result{Requeue: true, RequeueAfter: 10 * time.Second}, err
		}

		return nil, nil
	} else if err != nil {
		req.Logger.Error(err, "failed to get service", "service", key)
		return &ctrl.Result{Requeue: true, RequeueAfter: 10 * time.Second}, err
	}

	return nil, nil
}

func GetServiceName(reaperName string) string {
	return reaperName + "-reaper-service"
}

func newService(key types.NamespacedName, reaper *api.Reaper) *corev1.Service {
	labels := createLabels(reaper)

	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Service",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port:     8080,
					Name:     "app",
					Protocol: corev1.ProtocolTCP,
					TargetPort: intstr.IntOrString{
						Type:   intstr.String,
						StrVal: "app",
					},
				},
			},
			Selector: labels,
		},
	}
}

func (r *defaultReconciler) ReconcileSchema(ctx context.Context, req ReaperRequest) (*ctrl.Result, error) {
	reaper := req.Reaper
	key := types.NamespacedName{Namespace: reaper.Namespace, Name: getSchemaJobName(reaper)}

	req.Logger.Info("reconciling schema", "job", key)

	if reaper.Spec.ServerConfig.StorageType == api.StorageTypeMemory {
		// No need to run schema job when using in-memory backend
		return nil, nil
	}

	schemaJob := &v1batch.Job{}
	err := r.Client.Get(ctx, key, schemaJob)
	if err != nil && errors.IsNotFound(err) {
		// create the job
		schemaJob = newSchemaJob(reaper)
		if err = controllerutil.SetControllerReference(reaper, schemaJob, r.scheme); err != nil {
			req.Logger.Error(err, "failed to set owner on schema job", "job", key)
			return &ctrl.Result{Requeue: true, RequeueAfter: 10 * time.Second}, err
		}
		req.Logger.Info("creating schema job", "job", key)
		if err = r.Client.Create(ctx, schemaJob); err != nil {
			req.Logger.Error(err, "failed to create schema job", "job", key)
			return &ctrl.Result{Requeue: true, RequeueAfter: 10 * time.Second}, err
		} else {
			return &ctrl.Result{Requeue: true, RequeueAfter: 10 * time.Second}, err
		}
	} else if !jobFinished(schemaJob) {
		req.Logger.Info("schema job not finished", "job", key)
		return &ctrl.Result{Requeue: true, RequeueAfter: 5 * time.Second}, nil
	} else if failed, err := jobFailed(schemaJob); failed {
		return &ctrl.Result{Requeue: true, RequeueAfter: 10 * time.Second}, err
	} else {
		// the job completed successfully
		return nil, nil
	}
}

func getSchemaJobName(r *api.Reaper) string {
	return fmt.Sprintf("%s-schema", r.Name)
}

func newSchemaJob(reaper *api.Reaper) *v1batch.Job {
	cassandra := *reaper.Spec.ServerConfig.CassandraBackend
	return &v1batch.Job{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Job",
			APIVersion: "batch/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: reaper.Namespace,
			Name:      getSchemaJobName(reaper),
			Labels:    createLabels(reaper),
		},
		Spec: v1batch.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyOnFailure,
					Containers: []corev1.Container{
						{
							Name:            getSchemaJobName(reaper),
							Image:           schemaJobImage,
							ImagePullPolicy: schemaJobImagePullPolicy,
							Env: []corev1.EnvVar{
								{
									Name:  "KEYSPACE",
									Value: cassandra.Keyspace,
								},
								{
									Name:  "CONTACT_POINTS",
									Value: getCassandraServiceName(reaper),
								},
								{
									Name:  "REPLICATION",
									Value: config.ReplicationToString(cassandra.Replication),
								},
							},
						},
					},
				},
			},
		},
	}
}

func jobFinished(job *v1batch.Job) bool {
	for _, c := range job.Status.Conditions {
		if (c.Type == v1batch.JobComplete || c.Type == v1batch.JobFailed) && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func jobFailed(job *v1batch.Job) (bool, error) {
	for _, cond := range job.Status.Conditions {
		if cond.Type == v1batch.JobFailed && cond.Status == corev1.ConditionTrue {
			return true, fmt.Errorf("schema job failed: %s", cond.Message)
		}
	}
	return false, nil
}

func (r *defaultReconciler) ReconcileDeployment(ctx context.Context, req ReaperRequest) (*ctrl.Result, error) {
	reaper := req.Reaper
	key := types.NamespacedName{Namespace: reaper.Namespace, Name: reaper.Name}

	req.Logger.Info("reconciling deployment", "deployment", key)

	deployment := &appsv1.Deployment{}
	err := r.Get(ctx, key, deployment)
	if err != nil && errors.IsNotFound(err) {
		// create the deployment
		deployment = newDeployment(reaper)

		if isJmxAuthEnabled(reaper) {
			secretKey := getJmxAuthSecretRefKey(reaper)
			secret, err := r.getSecret(secretKey)
			if err != nil {
				req.Logger.Error(err, "failed to get jmxUserSecret", "deployment", key)
				return &ctrl.Result{RequeueAfter: 10 * time.Second}, err
			}

			if usernameEnvVar, passwordEnvVar, err := r.secretsManager.GetJmxAuthCredentials(secret); err == nil {
				addJmxAuthEnvVars(deployment, usernameEnvVar, passwordEnvVar)
			} else {
				req.Logger.Error(err, "failed to get JMX credentials", "deployment", key)
				return &ctrl.Result{RequeueAfter: 10 * time.Second}, err
			}
		}

		if err = controllerutil.SetControllerReference(reaper, deployment, r.scheme); err != nil {
			req.Logger.Error(err, "failed to set owner on deployment", "deployment", key)
			return &ctrl.Result{Requeue: true, RequeueAfter: 10 * time.Second}, err
		}
		req.Logger.Info("creating deployment", "deployment", key)
		if err = r.Create(ctx, deployment); err != nil {
			req.Logger.Error(err, "failed to create deployment", "deployment", key)
			return &ctrl.Result{Requeue: true, RequeueAfter: 10 * time.Second}, err
		} else {
			return &ctrl.Result{Requeue: true, RequeueAfter: 10 * time.Second}, nil
		}
	} else if !isDeploymentReady(deployment) {
		req.Logger.Info("deployment not ready", "deployment", key)
		if err := req.StatusManager.SetNotReady(ctx, reaper); err != nil {
			req.Logger.Error(err, "deployment is not ready, failed to update reaper status", "deployment", key)
			return &ctrl.Result{Requeue: true, RequeueAfter: 10 * time.Second}, err
		}
		return &ctrl.Result{Requeue: true, RequeueAfter: 10 * time.Second}, nil
	}

	return nil, nil
}

func isJmxAuthEnabled(reaper *api.Reaper) bool {
	return reaper.Spec.ServerConfig.JmxUserSecret != nil
}

func getJmxAuthSecretRefKey(reaper *api.Reaper) types.NamespacedName {
	if len(reaper.Spec.ServerConfig.JmxUserSecret.Namespace) == 0 {
		return types.NamespacedName{Namespace: reaper.Namespace, Name: reaper.Spec.ServerConfig.JmxUserSecret.Name}
	} else {
		return types.NamespacedName{Namespace: reaper.Spec.ServerConfig.JmxUserSecret.Namespace, Name: reaper.Spec.ServerConfig.JmxUserSecret.Name}
	}
}

func addJmxAuthEnvVars(deployment *appsv1.Deployment, usernameEnvVar, passwordEnvVar *corev1.EnvVar) {
	envVars := deployment.Spec.Template.Spec.Containers[0].Env
	envVars = append(envVars, *usernameEnvVar)
	envVars = append(envVars, *passwordEnvVar)
	deployment.Spec.Template.Spec.Containers[0].Env = envVars
}

func newDeployment(reaper *api.Reaper) *appsv1.Deployment {
	labels := createLabels(reaper)

	selector := metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{
				Key:      mlabels.ManagedByLabel,
				Operator: metav1.LabelSelectorOpIn,
				Values:   []string{mlabels.ManagedByLabelValue},
			},
			{
				Key:      mlabels.ReaperLabel,
				Operator: metav1.LabelSelectorOpIn,
				Values:   []string{reaper.Name},
			},
		},
	}

	healthProbe := &corev1.Probe{
		Handler: corev1.Handler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/healthcheck",
				Port: intstr.FromInt(8081),
			},
		},
		InitialDelaySeconds: 45,
		PeriodSeconds:       15,
	}

	envVars := make([]corev1.EnvVar, 0)
	if reaper.Spec.ServerConfig.CassandraBackend != nil {
		envVars = []corev1.EnvVar{
			{
				Name:  "REAPER_STORAGE_TYPE",
				Value: "cassandra",
			},
			{
				Name:  "REAPER_ENABLE_DYNAMIC_SEED_LIST",
				Value: "false",
			},
			{
				Name:  "REAPER_CASS_CONTACT_POINTS",
				Value: fmt.Sprintf("[%s]", getCassandraServiceName(reaper)),
			},
			{
				Name:  "REAPER_AUTH_ENABLED",
				Value: "false",
			},
		}
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: reaper.Namespace,
			Name:      reaper.Name,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &selector,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:            "reaper",
							ImagePullPolicy: corev1.PullIfNotPresent,
							Image:           reaper.Spec.Image,
							Ports: []corev1.ContainerPort{
								{
									Name:          "app",
									ContainerPort: 8080,
									Protocol:      "TCP",
								},
								{
									Name:          "admin",
									ContainerPort: 8081,
									Protocol:      "TCP",
								},
							},
							LivenessProbe:  healthProbe,
							ReadinessProbe: healthProbe,
							Env:            envVars,
						},
					},
				},
			},
		},
	}
}

func getCassandraServiceName(reaper *api.Reaper) string {
	return reaper.Spec.ServerConfig.CassandraBackend.CassandraService + "." + reaper.Spec.ServerConfig.CassandraBackend.Namespace
}

func isDeploymentReady(deployment *appsv1.Deployment) bool {
	return deployment.Status.ReadyReplicas == 1
}

func createLabels(r *api.Reaper) map[string]string {
	labels := make(map[string]string)
	labels[mlabels.ReaperLabel] = r.Name
	mlabels.SetOperatorLabels(labels)

	return labels
}

func (r *defaultReconciler) getSecret(key types.NamespacedName) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	err := r.Get(context.Background(), key, secret)

	return secret, err
}
