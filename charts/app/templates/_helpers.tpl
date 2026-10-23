{{/*
=============================================================================
Template helpers.

Files beginning with an underscore are NOT rendered into Kubernetes manifests;
Helm treats them as partial libraries. Everything here exists so that naming
and labelling are defined exactly once. The moment two templates compute a
name differently you get a Service whose selector matches nothing, and that
failure is completely silent - the Service exists, has no endpoints, and
returns connection refused.
=============================================================================
*/}}

{{/*
app.name
The chart name, overridable. Truncated to 63 characters because that is the
hard limit on a Kubernetes label VALUE, and this string ends up in
app.kubernetes.io/name.

trimSuffix "-" handles the edge case where truncation lands exactly on a
hyphen: "my-very-long-name-" is not a valid DNS label, and the API server
rejects it with an error that does not mention truncation at all.
*/}}
{{- define "app.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
app.fullname
The name used for the actual Kubernetes objects: "<release>-<chart>".

Three cases:
  1. fullnameOverride set        -> use it verbatim
  2. release name already contains the chart name -> don't stutter
     (release "app" + chart "app" would otherwise give "app-app")
  3. otherwise                   -> "<release>-<name>"

63 characters again: object names can technically be 253 characters, but this
name is also used as a label value (and as the StatefulSet pod-name prefix in
charts that have one), so 63 is the safe common denominator.
*/}}
{{- define "app.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
app.chart
"<chart>-<version>" for the helm.sh/chart label.

The `replace "+" "_"` is not cosmetic: SemVer build metadata uses "+"
(1.0.0+build.5) and "+" is not a legal character in a Kubernetes label value.
Omit this and any chart with build metadata fails to install with a validation
error about the label, which is a very confusing place to end up.
*/}}
{{- define "app.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
app.labels
The full recommended label set, applied to every object the chart creates.

These are the app.kubernetes.io/* labels from the Kubernetes "recommended
labels" convention. They are what makes `kubectl get all -l
app.kubernetes.io/instance=<release>` return your whole application, what lets
Argo CD group resources into an Application tree, and what dashboards and cost
tools key off. Inventing your own label scheme instead means none of that
tooling can see your workload.

app.kubernetes.io/version carries the app version. Note it is quoted and
sanitised the same way as the chart label, for the same "+" reason.
*/}}
{{- define "app.labels" -}}
helm.sh/chart: {{ include "app.chart" . }}
{{ include "app.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | replace "+" "_" | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: gitops-cicd-platform
app.kubernetes.io/component: api
{{- end -}}

{{/*
app.selectorLabels
The SELECTOR subset - deliberately minimal and deliberately separate.

A Deployment's spec.selector.matchLabels is IMMUTABLE after creation. If the
selector included app.kubernetes.io/version, then every application upgrade
would change the selector, and the API server would reject the update with:

  "field is immutable"

leaving you unable to upgrade without deleting the Deployment - which means
downtime. So the selector contains only the two labels that identify the
workload and never change: name and instance.

This is the single most important structural decision in a Helm chart, and it
is invisible until the day it bites.
*/}}
{{- define "app.selectorLabels" -}}
app.kubernetes.io/name: {{ include "app.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
app.serviceAccountName
The ServiceAccount to attach to the pod. When serviceAccount.create is false
we fall back to "default" rather than to the generated name, because
referencing a ServiceAccount that does not exist leaves the pod stuck in
ContainerCreating with a serviceaccount-not-found event.
*/}}
{{- define "app.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "app.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
app.image
Builds the full image reference, preferring the immutable digest over the
mutable tag.

  digest set  -> registry/repository@sha256:...
  digest unset-> registry/repository:tag   (tag defaults to Chart.appVersion)

Centralising this means the "prefer digest" policy is expressed once. If it
were inlined in deployment.yaml, the test pod or an init container added later
would quietly use a different resolution rule.
*/}}
{{- define "app.image" -}}
{{- $registry := .Values.image.registry | default "docker.io" -}}
{{- $repository := .Values.image.repository -}}
{{- if .Values.image.digest -}}
{{- printf "%s/%s@%s" $registry $repository .Values.image.digest -}}
{{- else -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- end -}}
{{- end -}}

{{/*
app.configMapName
The ConfigMap holding the env block. Named separately so the deployment's
checksum annotation and the envFrom reference cannot drift apart.
*/}}
{{- define "app.configMapName" -}}
{{- printf "%s-config" (include "app.fullname" .) -}}
{{- end -}}

{{/*
app.validateValues
Cross-field validation that a JSON Schema cannot express.

values.schema.json checks each field in isolation - it cannot say "field A
must be greater than the sum of fields B and C". These are the invariants
where getting it wrong produces a subtly broken deployment rather than an
obvious error, so we fail the render instead.

`fail` aborts `helm template` / `helm install` with the given message, which
in this repo means it aborts CI, which means it never reaches a cluster.
*/}}
{{- define "app.validateValues" -}}
{{- if and .Values.autoscaling.enabled (lt (int .Values.autoscaling.maxReplicas) (int .Values.autoscaling.minReplicas)) -}}
{{- fail (printf "autoscaling.maxReplicas (%d) must be >= autoscaling.minReplicas (%d)" (int .Values.autoscaling.maxReplicas) (int .Values.autoscaling.minReplicas)) -}}
{{- end -}}
{{- if and .Values.podDisruptionBudget.enabled .Values.podDisruptionBudget.minAvailable .Values.podDisruptionBudget.maxUnavailable -}}
{{- fail "podDisruptionBudget: set exactly one of minAvailable or maxUnavailable, not both - the API server rejects a PDB with both" -}}
{{- end -}}
{{- if and .Values.securityContext.readOnlyRootFilesystem (not .Values.tmpDir.enabled) -}}
{{- fail "securityContext.readOnlyRootFilesystem is true but tmpDir.enabled is false - the container will have nowhere to write scratch files and will CrashLoopBackOff" -}}
{{- end -}}
{{- if .Values.ingress.enabled -}}
{{- if not .Values.ingress.hosts -}}
{{- fail "ingress.enabled is true but ingress.hosts is empty - the rendered Ingress would match no traffic" -}}
{{- end -}}
{{- end -}}
{{- end -}}
