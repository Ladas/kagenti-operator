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

package v1alpha1

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	agentv1alpha1 "github.com/kagenti/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var agentbuildlog = ctrl.Log.WithName("agentcard-webhook").WithName("webhook")

const (
	// ConfigMap naming convention for pipeline templates
	PipelineTemplateConfigMapPrefix = "pipeline-template"
	operatorNamespaceEnv            = "POD_NAMESPACE"
	// Default values
	DefaultPipelineMode = "dev"
)

// SetupAgentBuildWebhookWithManager will setup the manager to manage the webhooks
func SetupAgentBuildWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&agentv1alpha1.AgentBuild{}).
		WithDefaulter(&AgentBuildDefaulter{Client: mgr.GetClient()}).
		WithValidator(&AgentBuildValidator{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

//+kubebuilder:webhook:path=/mutate-agent-kagenti-dev-v1alpha1-agentbuild,mutating=true,failurePolicy=fail,sideEffects=None,groups=agent.kagenti.dev,resources=agentbuilds,verbs=create;update,versions=v1alpha1,name=magentbuild.kb.io,admissionReviewVersions=v1

// AgentBuildDefaulter implements defaulting webhook for AgentBuild
type AgentBuildDefaulter struct {
	Client client.Client
}

// Default implements the defaulting webhook
func (d *AgentBuildDefaulter) Default(ctx context.Context, obj runtime.Object) error {
	agentbuild, ok := obj.(*agentv1alpha1.AgentBuild)
	if !ok {
		return fmt.Errorf("expected an AgentBuild but got a %T", obj)
	}

	agentbuildlog.Info("Applying defaults", "name", agentbuild.Name, "namespace", agentbuild.Namespace)

	// Apply basic defaults
	d.applyBasicDefaults(agentbuild)

	// Auto-generate pipeline parameters from spec
	d.autoGenerateParameters(agentbuild)

	// Inject pipeline template if needed
	if d.shouldInjectPipelineTemplate(agentbuild) {
		if err := d.processPipelineConfig(ctx, agentbuild); err != nil {
			return fmt.Errorf("failed to process pipeline config: %w", err)
		}
	}

	return nil
}

// sets default values for basic fields
func (d *AgentBuildDefaulter) applyBasicDefaults(agentbuild *agentv1alpha1.AgentBuild) {
	// Default mode
	if agentbuild.Spec.Mode == "" {
		agentbuild.Spec.Mode = DefaultPipelineMode
	}

	// Ensure pipeline spec exists
	if agentbuild.Spec.Pipeline == nil {
		agentbuild.Spec.Pipeline = &agentv1alpha1.PipelineSpec{}
	}

	// Default pipeline namespace to AgentBuild's namespace
	if agentbuild.Spec.Pipeline.Namespace == "" {
		agentbuild.Spec.Pipeline.Namespace = operatorNamespace(agentbuild.Namespace)
	}

	// Default source revision
	if agentbuild.Spec.SourceSpec.SourceRevision == "" {
		agentbuild.Spec.SourceSpec.SourceRevision = "main"
	}
}
func operatorNamespace(defaultNS string) string {
	if ns, ok := os.LookupEnv(operatorNamespaceEnv); ok && ns != "" {
		return ns
	}
	return defaultNS
}
func (d *AgentBuildDefaulter) autoGenerateParameters(agentbuild *agentv1alpha1.AgentBuild) {
	// Build map of existing parameters for efficient lookup
	existingParams := make(map[string]bool)
	for _, param := range agentbuild.Spec.Pipeline.Parameters {
		existingParams[param.Name] = true
	}

	// Helper to add parameter if not exists
	addParam := func(name, value string) {
		if !existingParams[name] && value != "" {
			agentbuild.Spec.Pipeline.Parameters = append(agentbuild.Spec.Pipeline.Parameters,
				agentv1alpha1.ParameterSpec{
					Name:  name,
					Value: value,
				})
			agentbuildlog.V(1).Info("Auto-generated parameter", "name", name, "value", value)
		}
	}

	// Generate source parameters
	addParam("repo-url", agentbuild.Spec.SourceSpec.SourceRepository)
	addParam("revision", agentbuild.Spec.SourceSpec.SourceRevision)
	addParam("subfolder-path", agentbuild.Spec.SourceSpec.SourceSubfolder)

	// Generate image parameter if buildOutput specified
	if agentbuild.Spec.BuildOutput != nil {
		imageURL := fmt.Sprintf("%s/%s:%s",
			agentbuild.Spec.BuildOutput.ImageRegistry,
			agentbuild.Spec.BuildOutput.Image,
			agentbuild.Spec.BuildOutput.ImageTag,
		)
		addParam("image", imageURL)

		// Add registry secret if specified
		if agentbuild.Spec.BuildOutput.ImageRepoCredentials != nil {
			addParam("registry-secret", agentbuild.Spec.BuildOutput.ImageRepoCredentials.Name)
		}
	}

	// Generate source credentials parameter if specified
	if agentbuild.Spec.SourceSpec.SourceCredentials != nil {
		addParam("SOURCE_REPO_SECRET", agentbuild.Spec.SourceSpec.SourceCredentials.Name)
	}
}

// determines if pipeline template should be injected
func (d *AgentBuildDefaulter) shouldInjectPipelineTemplate(agentbuild *agentv1alpha1.AgentBuild) bool {
	return agentbuild.Spec.Pipeline == nil || agentbuild.Spec.Pipeline.Steps == nil || len(agentbuild.Spec.Pipeline.Steps) == 0
}

func (d *AgentBuildDefaulter) processPipelineConfig(ctx context.Context, agentbuild *agentv1alpha1.AgentBuild) error {

	var buildSpec = &agentbuild.Spec

	agentbuildlog.Info("Mutating webhook - injecting Tekton pipeline", "name", agentbuild.GetName())

	// Skip if custom pipeline is provided
	if buildSpec.Pipeline.Steps != nil {
		agentbuildlog.Info("Mutating webhook - using user defined Tekton pipeline", "name", agentbuild.GetName())
		return nil
	}

	// Get pipeline mode (default to "dev" if not specified)
	mode := buildSpec.Mode
	if mode == "" {
		mode = DefaultPipelineMode
		buildSpec.Mode = mode
	}
	namespace := agentbuild.Namespace
	if agentbuild.Spec.Pipeline != nil && agentbuild.Spec.Pipeline.Namespace != "" {
		namespace = agentbuild.Spec.Pipeline.Namespace
	}

	// Load pipeline template from ConfigMap
	template, err := d.getPipelineTemplate(ctx, mode, namespace)
	if err != nil {
		return fmt.Errorf("failed to get pipeline template for mode %s: %w", mode, err)
	}
	// If user provided custom steps, merge whenExpressions from template
	if len(buildSpec.Pipeline.Steps) > 0 {
		agentbuildlog.Info("Mutating webhook - merging whenExpressions from template into user-defined steps")
		d.mergeWhenExpressionsIntoUserSteps(template, buildSpec.Pipeline)
		return nil
	}
	agentbuildlog.Info("Mutating webhook - using template steps")
	// Validate required parameters
	err = d.validateRequiredParameters(template, buildSpec.Pipeline)
	if err != nil {
		return fmt.Errorf("required parameter validation failed: %w", err)
	}

	// Create the final pipeline by merging template with user parameters
	pipelineSpec, err := d.mergePipelineTemplate(template, buildSpec.Pipeline)
	if err != nil {
		return fmt.Errorf("failed to merge pipeline template: %w", err)
	}

	// Replace the pipeline config with the final merged pipeline
	buildSpec.Pipeline.Steps = pipelineSpec.Steps

	agentbuildlog.Info("Mutating webhook - injected Tekton pipeline for AgentBuild", "name", agentbuild.GetName(), "agentbuild", agentbuild)
	return nil
}

// merges whenExpressions from template into user-provided steps
func (d *AgentBuildDefaulter) mergeWhenExpressionsIntoUserSteps(template *agentv1alpha1.PipelineTemplate, pipelineSpec *agentv1alpha1.PipelineSpec) {
	// Create map of template steps for easy lookup
	templateStepsMap := make(map[string]agentv1alpha1.PipelineStepTemplate)
	for _, templateStep := range template.Steps {
		templateStepsMap[templateStep.Name] = templateStep
	}

	// Merge whenExpressions from template into user's steps
	for i := range pipelineSpec.Steps {
		step := &pipelineSpec.Steps[i]

		if templateStep, exists := templateStepsMap[step.Name]; exists {
			// Copy whenExpressions from template if not specified by user
			if len(step.WhenExpressions) == 0 && len(templateStep.WhenExpressions) > 0 {
				step.WhenExpressions = templateStep.WhenExpressions
				agentbuildlog.Info("Merged whenExpressions from template",
					"stepName", step.Name,
					"count", len(step.WhenExpressions))
			}
			// Merge defaultParameters from template if not specified by user
			if len(step.Parameters) == 0 && len(templateStep.DefaultParameters) > 0 {
				step.Parameters = templateStep.DefaultParameters
				agentbuildlog.Info("Merged defaultParameters from template",
					"stepName", step.Name,
					"count", len(step.Parameters))

				// Log each parameter being merged
				for _, param := range step.Parameters {
					agentbuildlog.Info("Merged parameter",
						"stepName", step.Name,
						"paramName", param.Name,
						"paramValue", param.Value)
				}
			}
			// Also merge enabled flag if not specified
			if step.Enabled == nil && templateStep.Enabled != nil {
				step.Enabled = templateStep.Enabled
			}
		}
	}
}

// retrieves pipeline template from ConfigMap
func (d *AgentBuildDefaulter) getPipelineTemplate(ctx context.Context, mode, namespace string) (*agentv1alpha1.PipelineTemplate, error) {
	configMapName := fmt.Sprintf("%s-%s", PipelineTemplateConfigMapPrefix, mode)
	agentbuildlog.Info("Mutating webhook - using Tekton pipeline from configMap", "configMap Name", configMapName, "namespace", namespace)

	configMap := &corev1.ConfigMap{}
	err := d.Client.Get(ctx, types.NamespacedName{
		Name:      configMapName,
		Namespace: namespace,
	}, configMap)
	if err != nil {
		return nil, fmt.Errorf("failed to get ConfigMap %s/%s: %w", namespace, configMapName, err)
	}

	// Parse pipeline template from ConfigMap data
	templateData, exists := configMap.Data["template.json"]
	if !exists {
		return nil, fmt.Errorf("template.json not found in ConfigMap %s", configMapName)
	}
	agentbuildlog.Info("Mutating webhook - found Tekton pipeline in configMap", "configMap Name", configMapName)

	var template agentv1alpha1.PipelineTemplate

	err = json.Unmarshal([]byte(templateData), &template)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal pipeline template from ConfigMap: %w", err)
	}
	agentbuildlog.Info("Mutating webhook - unmarshalled Tekton pipeline from configMap", "configMap Name", configMapName)

	return &template, nil
}

func (d *AgentBuildDefaulter) validateRequiredParameters(template *agentv1alpha1.PipelineTemplate, pipelineSpec *agentv1alpha1.PipelineSpec) error {
	userParams := make(map[string]string)
	agentbuildlog.Info("Mutating webhook - validateRequiredParameters()")

	// Build map of user-provided parameters
	for _, param := range pipelineSpec.Parameters {
		userParams[param.Name] = param.Value
	}

	// Check global required parameters
	for _, requiredParam := range template.RequiredParameters {
		if _, exists := userParams[requiredParam]; !exists {
			return fmt.Errorf("required parameter '%s' not provided", requiredParam)
		}
	}

	// Check step-specific required parameters
	for _, step := range template.Steps {
		for _, requiredParam := range step.RequiredParameters {
			if _, exists := userParams[requiredParam]; !exists {
				return fmt.Errorf("required parameter '%s' not provided for step '%s'", requiredParam, step.Name)
			}
		}
	}

	return nil
}

// merges template with user parameters to create final pipeline
func (d *AgentBuildDefaulter) mergePipelineTemplate(template *agentv1alpha1.PipelineTemplate, pipelineSpec *agentv1alpha1.PipelineSpec) (*agentv1alpha1.PipelineSpec, error) {
	agentbuildlog.Info("Mutating webhook - mergePipelineTemplate()")
	// Create parameter map from user input
	userParams := make(map[string]string)
	for _, param := range pipelineSpec.Parameters {
		userParams[param.Name] = param.Value
	}

	var pipelineSteps []agentv1alpha1.PipelineStepSpec

	// Process each step in the template
	for _, stepTemplate := range template.Steps {
		// Skip disabled steps
		if stepTemplate.Enabled != nil && !*stepTemplate.Enabled {
			continue
		}

		// Create final step
		pipelineStep := agentv1alpha1.PipelineStepSpec{
			Name:            stepTemplate.Name,
			ConfigMap:       stepTemplate.ConfigMap,
			Enabled:         stepTemplate.Enabled,
			WhenExpressions: stepTemplate.WhenExpressions,
			Parameters:      stepTemplate.DefaultParameters,
		}
		agentbuildlog.Info("Adding pipeline step from template",
			"stepName", stepTemplate.Name,
			"configMap", stepTemplate.ConfigMap,
			"whenExpressionsCount", len(stepTemplate.WhenExpressions),
			"defaultParametersCount", len(stepTemplate.DefaultParameters))

		pipelineSteps = append(pipelineSteps, pipelineStep)
	}

	// Create the pipeline
	pipeline := &agentv1alpha1.PipelineSpec{
		Namespace: template.Namespace,
		Steps:     pipelineSteps,
	}

	// Add global parameters if any
	for _, globalParam := range template.GlobalParameters {
		resolvedValue := d.resolveTemplateValue(globalParam.Value, userParams)
		pipeline.Parameters = append(pipeline.Parameters, agentv1alpha1.ParameterSpec{
			Name:  globalParam.Name,
			Value: resolvedValue,
		})
	}

	return pipeline, nil
}

// resolves template variables in parameter values
func (d *AgentBuildDefaulter) resolveTemplateValue(value string, vars map[string]string) string {
	resolved := value

	// Simple template variable substitution: {{.VarName}}
	for varName, varValue := range vars {
		placeholder := fmt.Sprintf("{{.%s}}", varName)
		resolved = strings.ReplaceAll(resolved, placeholder, varValue)
	}

	return resolved
}

//+kubebuilder:webhook:path=/validate-agent-kagenti-dev-v1alpha1-agentbuild,mutating=false,failurePolicy=fail,sideEffects=None,groups=agent.kagenti.dev,resources=agentbuilds,verbs=create;update,versions=v1alpha1,name=vagentbuild.kb.io,admissionReviewVersions=v1

// AgentBuildValidator implements validating webhook for AgentBuild
type AgentBuildValidator struct{}

// ValidateCreate implements webhook validation for create
func (v *AgentBuildValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	agentbuild, ok := obj.(*agentv1alpha1.AgentBuild)
	if !ok {
		return nil, fmt.Errorf("expected an AgentBuild but got a %T", obj)
	}

	agentbuildlog.Info("validate create", "name", agentbuild.Name)

	return nil, v.validateAgentBuild(agentbuild)
}

// ValidateUpdate implements webhook validation for update
func (v *AgentBuildValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	agentbuild, ok := newObj.(*agentv1alpha1.AgentBuild)
	if !ok {
		return nil, fmt.Errorf("expected an AgentBuild but got a %T", newObj)
	}

	agentbuildlog.Info("validate update", "name", agentbuild.Name)

	return nil, v.validateAgentBuild(agentbuild)
}

// ValidateDelete implements webhook validation for delete
func (v *AgentBuildValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	agentbuild, ok := obj.(*agentv1alpha1.AgentBuild)
	if !ok {
		return nil, fmt.Errorf("expected an AgentBuild but got a %T", obj)
	}

	agentbuildlog.Info("validate delete", "name", agentbuild.Name)

	// Allow deletions
	return nil, nil
}

func (v *AgentBuildValidator) validateAgentBuild(agentbuild *agentv1alpha1.AgentBuild) error {
	// Validate source repository is specified
	if agentbuild.Spec.SourceSpec.SourceRepository == "" {
		return fmt.Errorf("spec.source.sourceRepository is required")
	}

	// Validate build output if specified
	if agentbuild.Spec.BuildOutput != nil {
		if agentbuild.Spec.BuildOutput.Image == "" {
			return fmt.Errorf("spec.buildOutput.image is required when buildOutput is specified")
		}
		if agentbuild.Spec.BuildOutput.ImageTag == "" {
			return fmt.Errorf("spec.buildOutput.imageTag is required when buildOutput is specified")
		}
		if agentbuild.Spec.BuildOutput.ImageRegistry == "" {
			return fmt.Errorf("spec.buildOutput.imageRegistry is required when buildOutput is specified")
		}
	}

	// Validate pipeline namespace is specified
	if agentbuild.Spec.Pipeline.Namespace == "" {
		return fmt.Errorf("spec.pipeline.namespace is required")
	}

	// Validate at least one pipeline step
	if len(agentbuild.Spec.Pipeline.Steps) == 0 {
		return fmt.Errorf("spec.pipeline.steps must contain at least one step")
	}

	// Validate each step has name and configMap
	for i, step := range agentbuild.Spec.Pipeline.Steps {
		if step.Name == "" {
			return fmt.Errorf("spec.pipeline.steps[%d].name is required", i)
		}
		if step.ConfigMap == "" {
			return fmt.Errorf("spec.pipeline.steps[%d].configMap is required", i)
		}
	}

	return nil
}
