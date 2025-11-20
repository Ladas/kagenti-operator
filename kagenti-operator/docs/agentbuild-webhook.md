# AgentBuild Webhook

This webhook automatically injects Tekton Pipeline specifications into AgentBuild Custom Resources (CRs) at creation time.
It ensures consistent pipeline structure by merging user-provided parameters with a shared Tekton pipeline template stored in a ConfigMap.

## Overview

The AgentBuild Mutating Webhook is part of the Kagenti Operator.
It performs two key functions:

1. Defaulting webhook (AgentBuildDefaulter)

    - Mutates the incoming AgentBuild CR by injecting a Tekton Pipeline specification.

    - Pulls the pipeline definition from a ConfigMap template based on build mode (dev, preprod, prod).

    - Adds missing default values and parameters (e.g., image name).

2. Validating webhook (AgentBuildValidator)

    - Validates required fields before allowing CR creation or updates.

## Architecture

```
User creates AgentBuild CR
         ↓
   API Server
         ↓
   Mutating Webhook (Defaulting)
         ↓
   Validating Webhook (Validation)
         ↓
   Persisted to etcd
         ↓
   AgentBuild Controller reconciles
```

## Defaulting Webhook

### Functionality

The `AgentBuildDefaulter` automatically populates default values for optional fields that users don't specify.

### Defaults Applied

| Field                        | Default Value      | Reason 
|------------------------------|--------------------|-----------------------------------
| `spec.mode`                  | `"dev"`            | Use development pipeline template.
| `spec.cleanupAfterBuild`     | `true`             | Clean up build resources after completion
| `spec.source.sourceRevision` | `"main"`           | Use main branch if not specified
| `spec.pipeline.namespace`    | `"kagenti-system"` | Look for pipeline step ConfigMaps in kagenti-system

### Webhook Logic Flow

When a new AgentBuild CR is created:

1. Defaults applied:

    - spec.mode -> defaults to "dev" if not provided.

    - spec.pipeline.namespace -> defaults to CR’s namespace.

    - Adds image parameter based on spec.buildOutput fields if missing.

2. Tekton pipeline injection:
    - Fetches ConfigMap named pipeline-template-<mode> (e.g., pipeline-template-dev).

    - Reads JSON payload under key template.json.

    - Validates that all required parameters in the template exist.

    - Merges user parameters with template to produce a final pipeline definition.

### Container Build Strategies

The operator supports two build strategies that are **automatically selected at runtime** using Tekton's conditional execution feature called **when expressions**.

#### 1. Dockerfile-based Build (Buildah)

Used when a `Dockerfile` or `Containerfile` exists in your repository.

**Advantages:**

- Full control over the build process
- Custom base images
- Multi-stage builds
- Explicit dependency management

**When Expression Condition:**

```yaml
when:
- input: "$(tasks.dockerfile-check.results.has-dockerfile)"
  operator: in
  values: ["true"]
```

#### 2. Buildpack-based Build (Cloud Native Buildpacks CNB)

Used when no `Dockerfile`or `Containerfile` is present. Supports:

- Python (detects `requirements.txt`, `Pipfile`, `pyproject.toml`)
- Go (detects `go.mod`)
- Node.js (detects `package.json`)
- Java (detects `pom.xml`, `build.gradle`)
- Ruby, PHP, .NET, Rust, and more

**When Expression Condition:**

```yaml
when:
- input: "$(tasks.dockerfile-check.results.has-dockerfile)"
  operator: in
  values: ["false"]
```

### Build Strategy Selection Logic

The operator makes the decision **dynamically** during pipeline execution:

**Pipeline Steps:**

```yaml
steps:
...
  - name: dockerfile-check          # Step 1: Detection
    task: check-dockerfile-step
    # Outputs: has-dockerfile result
  
  - name: buildah-build              # Step 2a: Conditional execution
    runAfter: ["dockerfile-check"]
    when:
    - input: "$(tasks.dockerfile-check.results.has-dockerfile)"
      operator: in
      values: ["true"]
    task: buildah-build-step
  
  - name: buildpack-step            # Step 2b: Conditional execution
    runAfter: ["dockerfile-check"]
    when:
    - input: "$(tasks.dockerfile-check.results.has-dockerfile)"
      operator: in
      values: ["false"]
    task: buildpack-step
```

#### Customizing Build Behavior

While the automatic selection works for most cases, you can customize the behavior:

**Override Start Command for Buildpacks:**

```yaml
apiVersion: agent.kagenti.dev/v1alpha1
kind: AgentBuild
metadata:
  name: my-python-app
spec:
  source:
    sourceRepository: github.com/example/python-app.git
  buildOutput:
    image: my-app
    imageTag: v1.0.0
    imageRegistry: registry.example.com
  pipeline:
    parameters:
    - name: START_COMMAND          # Custom start command
      value: "gunicorn app:app --workers 4"
    - name: PYTHON_VERSION         # Specify Python version
      value: "3.11"
```

### Pipeline Template Example

Each template ConfigMap is named pipeline-template-<mode> and must include a template.json key.

```json

apiVersion: v1
data:
  template.json: |
    {
      "name": "Development Pipeline",
      "namespace": "kagenti-system",
      "description": "Basic pipeline for development builds",
      "requiredParameters": [
        "repo-url",
        "revision",
        "subfolder-path",
        "image"
      ],
      "steps": [
        {
          "name": "github-clone",
          "configMap": "github-clone-step",
          "enabled": true,
          "description": "Clone source code from GitHub repository",
          "requiredParameters": ["repo-url"]
        },
        {
          "name": "folder-verification",
          "configMap": "check-subfolder-step",
          "enabled": true,
          "description": "Verify that the specified subfolder exists",
          "requiredParameters": ["subfolder-path"]
        },
        {
          "name": "dockerfile-check",
          "configMap": "check-dockerfile-step",
          "enabled": true,
          "description": "Checks if Dockerfile exists in the specified subfolder",
          "requiredParameters": ["subfolder-path"]
        },
        {
          "name": "buildah-build",
          "configMap": "buildah-build-step",
          "enabled": true,
          "description": "Build container image using buildah",
          "requiredParameters": ["image"],
          "whenExpressions": [
            {
              "input": "$(tasks.dockerfile-check.results.has-dockerfile)",
              "operator": "in",
              "values": ["true"]
            }
          ],
          "defaultParameters": [
            {
              "name": "DOCKERFILE",
              "value": "$(tasks.dockerfile-check.results.dockerfile-name)"
            }
          ]
        },   
        {
          "name": "buildpack-step",
          "configMap": "buildpack-step",
          "enabled": true,
          "description": "Build container image using Buildpacks",
          "requiredParameters": ["image"],
          "whenExpressions": [
            {
              "input": "$(tasks.dockerfile-check.results.has-dockerfile)",
              "operator": "in",
              "values": ["false"]
            }
          ]
        }
      ],
      "globalParameters": [
        {
          "name": "pipeline-timeout",
          "value": "20m",
          "description": "Overall pipeline timeout"
        }
      ]
    }
```