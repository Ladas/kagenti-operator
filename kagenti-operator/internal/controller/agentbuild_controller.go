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

	"github.com/kagenti/operator/internal/builder"

	corev1 "k8s.io/api/core/v1"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// AgentBuildReconciler reconciles a AgentBuild object
type AgentBuildReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Builder  builder.Builder
	Recorder record.EventRecorder
}

const (
	defaultRequeueDelay = 5 * time.Second
	buildCheckInterval  = 10 * time.Second
)

var (
	agentBuildlogger = ctrl.Log.WithName("controller").WithName("AgentBuild")
)

// +kubebuilder:rbac:groups=agent.kagenti.dev,resources=agentbuilds,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agent.kagenti.dev,resources=agentbuilds/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agent.kagenti.dev,resources=agentbuilds/finalizers,verbs=update
// +kubebuilder:rbac:groups=tekton.dev,resources=pipelines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=tekton.dev,resources=pipelineruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=tekton.dev,resources=taskruns,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the AgentBuild object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.0/pkg/reconcile
func (r *AgentBuildReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	agentBuildlogger := logger.WithValues("agentbuild", req.NamespacedName)
	agentBuildlogger.Info("Reconciling AgentBuild")
	agentBuild := &agentv1alpha1.AgentBuild{}
	err := r.Get(ctx, req.NamespacedName, agentBuild)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !agentBuild.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, agentBuild)
	}

	if !controllerutil.ContainsFinalizer(agentBuild, AGENT_FINALIZER) {
		controllerutil.AddFinalizer(agentBuild, AGENT_FINALIZER)
		if err := r.Update(ctx, agentBuild); err != nil {
			agentBuildlogger.Error(err, "Unable to add finalizer to AgentBuild")
			return ctrl.Result{}, err
		}
		// Initialize phase to Pending if empty (new AgentBuild)
		if agentBuild.Status.Phase == "" {
			agentBuild.Status.Phase = agentv1alpha1.BuildPhasePending
			agentBuild.Status.Message = "Build pending"
			if err := r.Status().Update(ctx, agentBuild); err != nil {
				agentBuildlogger.Error(err, "Failed to initialize AgentBuild status")
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		}
	}
	doBuild, err := r.triggerBuild(agentBuild)
	if err != nil {
		agentBuildlogger.Error(err, "Failed to determine if build is needed")
		return ctrl.Result{}, err
	}
	agentBuildlogger.Info("Controller --------", "buildNeeded", doBuild)
	if doBuild {
		if err := r.validateSpec(ctx, agentBuild); err != nil {
			agentBuildlogger.Error(err, "AgentBuild spec validation failed")
			r.Recorder.Event(agentBuild, corev1.EventTypeWarning, "ValidationFailed",
				fmt.Sprintf("Spec validation failed: %v", err))
			// Update status with validation error
			agentBuild.Status.Phase = agentv1alpha1.BuildPhaseFailed
			agentBuild.Status.Message = fmt.Sprintf("Validation failed: %v", err)
			if updateErr := r.Status().Update(ctx, agentBuild); updateErr != nil {
				agentBuildlogger.Error(updateErr, "Failed to update status after validation failure")
			}
			return ctrl.Result{}, err
		}
		err := r.Builder.Build(ctx, agentBuild)
		if err != nil {
			r.Recorder.Event(agentBuild, corev1.EventTypeWarning, "BuildFailed",
				fmt.Sprintf("Failed to start build: %v", err))
			logger.Error(err, "Failed to trigger build",
				"agentbuild", agentBuild.Name,
				"namespace", agentBuild.Namespace,
				"mode", agentBuild.Spec.Mode)
			return ctrl.Result{}, err
		}
		r.Recorder.Event(agentBuild, corev1.EventTypeNormal, "BuildStarted",
			fmt.Sprintf("PipelineRun %s created", agentBuild.Status.PipelineRunName))
		return ctrl.Result{RequeueAfter: defaultRequeueDelay}, nil
	}

	agentBuildlogger.Info("Controller --------", "build Phase", agentBuild.Status.Phase)
	switch agentBuild.Status.Phase {
	case agentv1alpha1.PhaseBuilding:
		previousPhase := agentBuild.Status.Phase
		err := r.Builder.CheckStatus(ctx, agentBuild)
		if err != nil {
			r.Recorder.Event(agentBuild, corev1.EventTypeWarning, "StatusCheckFailed",
				fmt.Sprintf("Failed to check build status: %v", err))
			agentBuildlogger.Error(err, "Failed to check agentbuild status", "AgentBuild", agentBuild.Name)
			return ctrl.Result{}, err
		}
		if previousPhase != agentBuild.Status.Phase {
			switch agentBuild.Status.Phase {
			case agentv1alpha1.BuildPhaseSucceeded:
				r.Recorder.Event(agentBuild, corev1.EventTypeNormal, "BuildSucceeded",
					fmt.Sprintf("Image built successfully: %s", agentBuild.Status.BuiltImage))
			case agentv1alpha1.BuildPhaseFailed:
				r.Recorder.Event(agentBuild, corev1.EventTypeWarning, "BuildFailed",
					agentBuild.Status.Message)
			}
		}
		return ctrl.Result{RequeueAfter: defaultRequeueDelay}, nil
	case agentv1alpha1.BuildPhaseSucceeded:
		err := r.Builder.Cleanup(ctx, agentBuild)
		if err != nil {
			r.Recorder.Event(agentBuild, corev1.EventTypeWarning, "CleanupFailed",
				fmt.Sprintf("Failed to cleanup resources: %v", err))
			agentBuildlogger.Error(err, "Failed to cleanup Builder resource", "AgentBuild", agentBuild.Name)
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}
func (r *AgentBuildReconciler) validateSpec(ctx context.Context, agentBuild *agentv1alpha1.AgentBuild) error {
	// Validate source repository
	if agentBuild.Spec.SourceSpec.SourceRepository == "" {
		return fmt.Errorf("sourceRepository is required")
	}

	// Validate build output if specified
	if agentBuild.Spec.BuildOutput != nil {
		if agentBuild.Spec.BuildOutput.Image == "" {
			return fmt.Errorf("buildOutput.image is required")
		}
	}

	// Validate secrets exist
	if agentBuild.Spec.SourceSpec.SourceCredentials != nil {
		secret := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      agentBuild.Spec.SourceSpec.SourceCredentials.Name,
			Namespace: agentBuild.Namespace,
		}, secret); err != nil {
			return fmt.Errorf("sourceCredentials secret not found: %w", err)
		}
	}
	// Validate registry credentials if specified
	if agentBuild.Spec.BuildOutput != nil &&
		agentBuild.Spec.BuildOutput.ImageRepoCredentials != nil {
		secret := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      agentBuild.Spec.BuildOutput.ImageRepoCredentials.Name,
			Namespace: agentBuild.Namespace,
		}, secret); err != nil {
			return fmt.Errorf("imageRepoCredentials secret not found: %w", err)
		}
	}
	return nil
}

func (r *AgentBuildReconciler) triggerBuild(agentBuild *agentv1alpha1.AgentBuild) (bool, error) {

	if agentBuild.Status.Phase == "" || agentBuild.Status.Phase == agentv1alpha1.BuildPhasePending {
		return true, nil
	}
	if agentBuild.Status.Phase == agentv1alpha1.BuildPhaseFailed {
		return true, nil
	}

	return false, nil
}
func (r *AgentBuildReconciler) handleDeletion(ctx context.Context, agentBuild *agentv1alpha1.AgentBuild) (ctrl.Result, error) {
	agentBuildlogger.Info("Controller --------", "handleDeletion", "Deleting AgentBuild")
	if controllerutil.ContainsFinalizer(agentBuild, AGENT_FINALIZER) {
		// Cleanup PipelineRun if exists
		if agentBuild.Status.PipelineRunName != "" {
			pipelineRun := &tektonv1.PipelineRun{}
			err := r.Get(ctx, types.NamespacedName{
				Name:      agentBuild.Status.PipelineRunName,
				Namespace: agentBuild.Namespace,
			}, pipelineRun)
			if err == nil {
				if err := r.Delete(ctx, pipelineRun); err != nil && !errors.IsNotFound(err) {
					return ctrl.Result{}, err
				}
			}
		}

		// Cleanup workspace PVC
		pvcName := fmt.Sprintf("%s-workspace", agentBuild.Name)
		pvc := &corev1.PersistentVolumeClaim{}
		err := r.Get(ctx, types.NamespacedName{
			Name:      pvcName,
			Namespace: agentBuild.Namespace,
		}, pvc)
		if err == nil {
			if err := r.Delete(ctx, pvc); err != nil && !errors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
		}

		// Remove finalizer
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			latestAgentBuild := &agentv1alpha1.AgentBuild{}
			if err := r.Get(ctx, types.NamespacedName{
				Name:      agentBuild.Name,
				Namespace: agentBuild.Namespace,
			}, latestAgentBuild); err != nil {
				return err
			}
			controllerutil.RemoveFinalizer(latestAgentBuild, AGENT_FINALIZER)
			return r.Update(ctx, latestAgentBuild)
		}); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentBuildReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentv1alpha1.AgentBuild{}).
		Owns(&tektonv1.PipelineRun{}).
		Owns(&tektonv1.Pipeline{}).
		Named("Agentbuild").
		Complete(r)
}
