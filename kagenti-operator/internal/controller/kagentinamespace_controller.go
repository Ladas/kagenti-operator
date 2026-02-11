/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
)

var kagentiNamespaceLogger = ctrl.Log.WithName("controller").WithName("KagentiNamespace")

const (
	KagentiNamespaceFinalizer = "kagentinamespace.kagenti.dev/finalizer"

	// Labels applied to provisioned namespaces.
	LabelKagentiEnabled     = "kagenti-enabled"
	LabelIstioDiscovery     = "istio-discovery"
	LabelIstioDataplane     = "istio.io/dataplane-mode"
	LabelIstioWaypoint      = "istio.io/use-waypoint"
	LabelSharedGateway      = "shared-gateway-access"
	LabelManagedByKagenti   = "kagenti.io/managed-by"
	LabelKagentiNamespaceCR = "kagenti.io/kagentinamespace"

	// Defaults.
	DefaultTemplateConfigMap = "kagenti-namespace-template"
	PlatformNamespace        = "kagenti-system"
)

// KagentiNamespaceReconciler reconciles a KagentiNamespace object.
type KagentiNamespaceReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=agent.kagenti.dev,resources=kagentinamespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agent.kagenti.dev,resources=kagentinamespaces/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agent.kagenti.dev,resources=kagentinamespaces/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create;update;patch

func (r *KagentiNamespaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := kagentiNamespaceLogger.WithValues("kagentinamespace", req.Name)
	log.Info("Reconciling KagentiNamespace")

	// Fetch the KagentiNamespace CR
	kns := &agentv1alpha1.KagentiNamespace{}
	if err := r.Get(ctx, req.NamespacedName, kns); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	namespaceName := kns.GetNamespaceName()

	// Handle deletion
	if !kns.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, kns, namespaceName)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(kns, KagentiNamespaceFinalizer) {
		controllerutil.AddFinalizer(kns, KagentiNamespaceFinalizer)
		if err := r.Update(ctx, kns); err != nil {
			log.Error(err, "Unable to add finalizer")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Update status to Provisioning
	if kns.Status.Phase == "" || kns.Status.Phase == agentv1alpha1.KagentiNamespacePhasePending {
		kns.Status.Phase = agentv1alpha1.KagentiNamespacePhaseProvisioning
		kns.Status.Namespace = namespaceName
		if err := r.Status().Update(ctx, kns); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Step 1: Create/ensure namespace
	if err := r.ensureNamespace(ctx, kns, namespaceName); err != nil {
		r.setCondition(kns, agentv1alpha1.ConditionNamespaceCreated, metav1.ConditionFalse, "Failed", err.Error())
		return r.updateStatusError(ctx, kns, err)
	}
	r.setCondition(kns, agentv1alpha1.ConditionNamespaceCreated, metav1.ConditionTrue, "Created", "Namespace exists")

	// Step 2: Copy platform secrets
	if kns.Spec.Secrets == nil || kns.Spec.Secrets.InheritPlatformSecrets {
		if err := r.ensurePlatformSecrets(ctx, kns, namespaceName); err != nil {
			r.setCondition(kns, agentv1alpha1.ConditionSecretsProvisioned, metav1.ConditionFalse, "Failed", err.Error())
			return r.updateStatusError(ctx, kns, err)
		}
	}
	r.setCondition(kns, agentv1alpha1.ConditionSecretsProvisioned, metav1.ConditionTrue, "Provisioned", "Secrets provisioned")

	// Step 3: Create environments ConfigMap
	if err := r.ensureEnvironmentsConfigMap(ctx, kns, namespaceName); err != nil {
		r.setCondition(kns, agentv1alpha1.ConditionConfigMapsProvisioned, metav1.ConditionFalse, "Failed", err.Error())
		return r.updateStatusError(ctx, kns, err)
	}
	r.setCondition(kns, agentv1alpha1.ConditionConfigMapsProvisioned, metav1.ConditionTrue, "Provisioned", "ConfigMaps provisioned")

	// Step 4: Create SPIRE config (if enabled)
	spireEnabled := kns.Spec.Spire == nil || kns.Spec.Spire.Enabled
	if spireEnabled {
		if err := r.ensureSpireConfig(ctx, kns, namespaceName); err != nil {
			r.setCondition(kns, agentv1alpha1.ConditionSpireConfigured, metav1.ConditionFalse, "Failed", err.Error())
			// SPIRE is optional, log but continue
			log.Error(err, "Failed to create SPIRE config, continuing")
		} else {
			r.setCondition(kns, agentv1alpha1.ConditionSpireConfigured, metav1.ConditionTrue, "Configured", "SPIRE helper config created")
		}
	}

	// Step 5: Create RBAC for backend cross-namespace access
	if err := r.ensureRBAC(ctx, kns, namespaceName); err != nil {
		r.setCondition(kns, agentv1alpha1.ConditionRBACConfigured, metav1.ConditionFalse, "Failed", err.Error())
		return r.updateStatusError(ctx, kns, err)
	}
	r.setCondition(kns, agentv1alpha1.ConditionRBACConfigured, metav1.ConditionTrue, "Configured", "RBAC bindings created")

	// All done - update status to Ready
	now := metav1.Now()
	kns.Status.Phase = agentv1alpha1.KagentiNamespacePhaseReady
	kns.Status.ProvisionedAt = &now
	if err := r.Status().Update(ctx, kns); err != nil {
		return ctrl.Result{}, err
	}

	r.Recorder.Event(kns, corev1.EventTypeNormal, "Provisioned",
		fmt.Sprintf("Namespace %s fully provisioned", namespaceName))

	log.Info("KagentiNamespace reconciled successfully", "namespace", namespaceName)
	return ctrl.Result{}, nil
}

// ensureNamespace creates the Kubernetes namespace with required labels.
func (r *KagentiNamespaceReconciler) ensureNamespace(ctx context.Context, kns *agentv1alpha1.KagentiNamespace, name string) error {
	ns := &corev1.Namespace{}
	err := r.Get(ctx, types.NamespacedName{Name: name}, ns)
	if err != nil {
		if !errors.IsNotFound(err) {
			return err
		}

		// Create namespace
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				Labels: map[string]string{
					LabelKagentiEnabled:     "true",
					LabelIstioDiscovery:     "enabled",
					LabelIstioDataplane:     "ambient",
					LabelSharedGateway:      "true",
					LabelManagedByKagenti:   "kagenti-operator",
					LabelKagentiNamespaceCR: kns.Name,
				},
			},
		}

		// Add waypoint label if enabled
		istioWaypoint := kns.Spec.Istio == nil || kns.Spec.Istio.WaypointEnabled
		if istioWaypoint {
			ns.Labels[LabelIstioWaypoint] = "waypoint"
		}

		// Disable ambient if explicitly set to false
		if kns.Spec.Istio != nil && !kns.Spec.Istio.AmbientEnabled {
			delete(ns.Labels, LabelIstioDataplane)
		}

		if err := r.Create(ctx, ns); err != nil {
			return fmt.Errorf("failed to create namespace %s: %w", name, err)
		}

		r.Recorder.Event(kns, corev1.EventTypeNormal, "NamespaceCreated",
			fmt.Sprintf("Created namespace %s", name))
		return nil
	}

	// Namespace exists - ensure labels are correct
	updated := false
	if ns.Labels == nil {
		ns.Labels = make(map[string]string)
	}

	requiredLabels := map[string]string{
		LabelKagentiEnabled:     "true",
		LabelIstioDiscovery:     "enabled",
		LabelIstioDataplane:     "ambient",
		LabelSharedGateway:      "true",
		LabelManagedByKagenti:   "kagenti-operator",
		LabelKagentiNamespaceCR: kns.Name,
	}

	istioWaypoint := kns.Spec.Istio == nil || kns.Spec.Istio.WaypointEnabled
	if istioWaypoint {
		requiredLabels[LabelIstioWaypoint] = "waypoint"
	}

	for k, v := range requiredLabels {
		if ns.Labels[k] != v {
			ns.Labels[k] = v
			updated = true
		}
	}

	if updated {
		if err := r.Update(ctx, ns); err != nil {
			return fmt.Errorf("failed to update namespace labels: %w", err)
		}
	}

	return nil
}

// ensurePlatformSecrets copies platform secrets from kagenti-system to the target namespace.
func (r *KagentiNamespaceReconciler) ensurePlatformSecrets(ctx context.Context, kns *agentv1alpha1.KagentiNamespace, targetNS string) error {
	log := kagentiNamespaceLogger.WithValues("namespace", targetNS)

	// List all secrets in kagenti-system that are labeled for propagation
	secretList := &corev1.SecretList{}
	if err := r.List(ctx, secretList,
		client.InNamespace(PlatformNamespace),
		client.MatchingLabels{"kagenti.io/propagate": "true"},
	); err != nil {
		return fmt.Errorf("failed to list platform secrets: %w", err)
	}

	for i := range secretList.Items {
		src := &secretList.Items[i]
		targetName := src.Labels["kagenti.io/target-name"]
		if targetName == "" {
			targetName = src.Name
		}

		// Check if overridden
		if r.isSecretOverridden(kns, targetName) {
			continue
		}

		if err := r.copySecret(ctx, src, targetNS, targetName); err != nil {
			log.Error(err, "Failed to copy secret", "secret", src.Name)
			return err
		}
	}

	// Handle overrides
	if kns.Spec.Secrets != nil {
		for _, override := range kns.Spec.Secrets.Overrides {
			srcSecret := &corev1.Secret{}
			key := types.NamespacedName{
				Namespace: override.SourceNamespace,
				Name:      override.SourceSecret,
			}
			if err := r.Get(ctx, key, srcSecret); err != nil {
				return fmt.Errorf("failed to get override secret %s/%s: %w", override.SourceNamespace, override.SourceSecret, err)
			}
			if err := r.copySecret(ctx, srcSecret, targetNS, override.Name); err != nil {
				return err
			}
		}
	}

	return nil
}

// copySecret copies a secret to the target namespace with a new name.
func (r *KagentiNamespaceReconciler) copySecret(ctx context.Context, src *corev1.Secret, targetNS, targetName string) error {
	existing := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Namespace: targetNS, Name: targetName}, existing)
	if err != nil {
		if !errors.IsNotFound(err) {
			return err
		}

		// Create new secret
		newSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      targetName,
				Namespace: targetNS,
				Labels: map[string]string{
					LabelManagedByKagenti: "kagenti-operator",
				},
			},
			Type: src.Type,
			Data: src.Data,
		}
		return r.Create(ctx, newSecret)
	}

	// Update existing secret if managed by us
	if existing.Labels[LabelManagedByKagenti] == "kagenti-operator" {
		existing.Data = src.Data
		existing.Type = src.Type
		return r.Update(ctx, existing)
	}

	return nil
}

// isSecretOverridden checks if a secret name is in the overrides list.
func (r *KagentiNamespaceReconciler) isSecretOverridden(kns *agentv1alpha1.KagentiNamespace, secretName string) bool {
	if kns.Spec.Secrets == nil {
		return false
	}
	for _, override := range kns.Spec.Secrets.Overrides {
		if override.Name == secretName {
			return true
		}
	}
	return false
}

// ensureEnvironmentsConfigMap creates the environments ConfigMap from the template.
func (r *KagentiNamespaceReconciler) ensureEnvironmentsConfigMap(ctx context.Context, kns *agentv1alpha1.KagentiNamespace, targetNS string) error {
	templateRef := DefaultTemplateConfigMap
	if kns.Spec.Secrets != nil && kns.Spec.Secrets.TemplateRef != "" {
		templateRef = kns.Spec.Secrets.TemplateRef
	}

	// Try to read the template ConfigMap from kagenti-system
	templateCM := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Namespace: PlatformNamespace, Name: templateRef}, templateCM)
	if err != nil {
		if errors.IsNotFound(err) {
			// If template doesn't exist, try copying from an existing namespace (team1)
			return r.copyConfigMapFromExisting(ctx, "environments", targetNS)
		}
		return fmt.Errorf("failed to get template ConfigMap: %w", err)
	}

	// Create or update the environments ConfigMap
	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: targetNS, Name: "environments"}
	if err := r.Get(ctx, key, cm); err != nil {
		if !errors.IsNotFound(err) {
			return err
		}

		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "environments",
				Namespace: targetNS,
				Labels: map[string]string{
					LabelManagedByKagenti: "kagenti-operator",
				},
			},
			Data: templateCM.Data,
		}
		return r.Create(ctx, cm)
	}

	// Update if managed by us
	if cm.Labels[LabelManagedByKagenti] == "kagenti-operator" {
		cm.Data = templateCM.Data
		return r.Update(ctx, cm)
	}

	return nil
}

// copyConfigMapFromExisting copies a ConfigMap from an existing kagenti namespace.
func (r *KagentiNamespaceReconciler) copyConfigMapFromExisting(ctx context.Context, cmName, targetNS string) error {
	// Find an existing kagenti namespace to copy from
	nsList := &corev1.NamespaceList{}
	if err := r.List(ctx, nsList, client.MatchingLabels{LabelKagentiEnabled: "true"}); err != nil {
		return fmt.Errorf("failed to list kagenti namespaces: %w", err)
	}

	for i := range nsList.Items {
		ns := &nsList.Items[i]
		if ns.Name == targetNS {
			continue
		}

		srcCM := &corev1.ConfigMap{}
		if err := r.Get(ctx, types.NamespacedName{Namespace: ns.Name, Name: cmName}, srcCM); err == nil {
			newCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      cmName,
					Namespace: targetNS,
					Labels: map[string]string{
						LabelManagedByKagenti: "kagenti-operator",
					},
				},
				Data: srcCM.Data,
			}

			existing := &corev1.ConfigMap{}
			if err := r.Get(ctx, types.NamespacedName{Namespace: targetNS, Name: cmName}, existing); err != nil {
				if errors.IsNotFound(err) {
					return r.Create(ctx, newCM)
				}
				return err
			}
			if existing.Labels[LabelManagedByKagenti] == "kagenti-operator" {
				existing.Data = srcCM.Data
				return r.Update(ctx, existing)
			}
			return nil
		}
	}

	return fmt.Errorf("no existing kagenti namespace found to copy %s from", cmName)
}

// ensureSpireConfig creates the SPIRE helper ConfigMap.
func (r *KagentiNamespaceReconciler) ensureSpireConfig(ctx context.Context, kns *agentv1alpha1.KagentiNamespace, targetNS string) error {
	helperConf := `agent_address = "/spiffe-workload-api/spire-agent.sock"
cmd = ""
cmd_args = ""
svid_file_name = "/opt/svid.pem"
svid_key_file_name = "/opt/svid_key.pem"
svid_bundle_file_name = "/opt/svid_bundle.pem"
jwt_svids = [{jwt_audience="kagenti", jwt_svid_file_name="/opt/jwt_svid.token"}]
jwt_svid_file_mode = 0644
include_federated_domains = true`

	cm := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: targetNS, Name: "spiffe-helper-config"}
	if err := r.Get(ctx, key, cm); err != nil {
		if !errors.IsNotFound(err) {
			return err
		}

		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "spiffe-helper-config",
				Namespace: targetNS,
				Labels: map[string]string{
					LabelManagedByKagenti: "kagenti-operator",
				},
			},
			Data: map[string]string{
				"helper.conf": helperConf,
			},
		}
		return r.Create(ctx, cm)
	}

	return nil
}

// ensureRBAC creates RoleBindings for the backend to access the new namespace.
func (r *KagentiNamespaceReconciler) ensureRBAC(ctx context.Context, kns *agentv1alpha1.KagentiNamespace, targetNS string) error {
	rbName := "kagenti-backend-access"

	rb := &rbacv1.RoleBinding{}
	key := types.NamespacedName{Namespace: targetNS, Name: rbName}
	if err := r.Get(ctx, key, rb); err != nil {
		if !errors.IsNotFound(err) {
			return err
		}

		rb = &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      rbName,
				Namespace: targetNS,
				Labels: map[string]string{
					LabelManagedByKagenti: "kagenti-operator",
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     "view",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      "kagenti-backend",
					Namespace: PlatformNamespace,
				},
			},
		}
		return r.Create(ctx, rb)
	}

	return nil
}

// handleDeletion handles cleanup when a KagentiNamespace is deleted.
func (r *KagentiNamespaceReconciler) handleDeletion(ctx context.Context, kns *agentv1alpha1.KagentiNamespace, namespaceName string) (ctrl.Result, error) {
	log := kagentiNamespaceLogger.WithValues("kagentinamespace", kns.Name)

	if controllerutil.ContainsFinalizer(kns, KagentiNamespaceFinalizer) {
		// Note: We intentionally do NOT delete the namespace on CR deletion.
		// The namespace may contain running workloads that should not be destroyed.
		// Administrators must delete the namespace manually if desired.
		log.Info("KagentiNamespace being deleted. Namespace preserved for safety.",
			"namespace", namespaceName)

		r.Recorder.Event(kns, corev1.EventTypeNormal, "Cleanup",
			fmt.Sprintf("KagentiNamespace CR deleted. Namespace %s preserved (delete manually if needed)", namespaceName))

		controllerutil.RemoveFinalizer(kns, KagentiNamespaceFinalizer)
		if err := r.Update(ctx, kns); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// setCondition updates a condition on the KagentiNamespace status.
func (r *KagentiNamespaceReconciler) setCondition(kns *agentv1alpha1.KagentiNamespace, condType string, status metav1.ConditionStatus, reason, message string) {
	now := metav1.Now()
	for i, c := range kns.Status.Conditions {
		if c.Type == condType {
			if c.Status != status {
				kns.Status.Conditions[i].Status = status
				kns.Status.Conditions[i].Reason = reason
				kns.Status.Conditions[i].Message = message
				kns.Status.Conditions[i].LastTransitionTime = now
			}
			return
		}
	}

	kns.Status.Conditions = append(kns.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
	})
}

// updateStatusError updates the status to Error phase and returns.
func (r *KagentiNamespaceReconciler) updateStatusError(ctx context.Context, kns *agentv1alpha1.KagentiNamespace, origErr error) (ctrl.Result, error) {
	kns.Status.Phase = agentv1alpha1.KagentiNamespacePhaseError
	if err := r.Status().Update(ctx, kns); err != nil {
		kagentiNamespaceLogger.Error(err, "Failed to update error status")
	}
	r.Recorder.Event(kns, corev1.EventTypeWarning, "ProvisioningFailed", origErr.Error())
	return ctrl.Result{RequeueAfter: 30 * time.Second}, origErr
}

// SetupWithManager sets up the controller with the Manager.
func (r *KagentiNamespaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentv1alpha1.KagentiNamespace{}).
		Named("KagentiNamespace").
		Complete(r)
}
