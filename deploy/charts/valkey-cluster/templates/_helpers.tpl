{{/* ------------------------------------------------------------------------
     Naming
   ------------------------------------------------------------------------ */}}

{{- define "valkey-cluster.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "valkey-cluster.fullname" -}}
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

{{- define "valkey-cluster.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "valkey-cluster.headlessServiceName" -}}
{{- printf "%s-headless" (include "valkey-cluster.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "valkey-cluster.metricsServiceName" -}}
{{- printf "%s-metrics" (include "valkey-cluster.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "valkey-cluster.configMapName" -}}
{{- printf "%s-config" (include "valkey-cluster.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "valkey-cluster.scriptsConfigMapName" -}}
{{- printf "%s-scripts" (include "valkey-cluster.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "valkey-cluster.secretName" -}}
{{- if .Values.auth.existingSecret -}}
{{- .Values.auth.existingSecret -}}
{{- else -}}
{{- printf "%s-auth" (include "valkey-cluster.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "valkey-cluster.secretPasswordKey" -}}
{{- if .Values.auth.existingSecret -}}
{{- .Values.auth.existingSecretPasswordKey -}}
{{- else -}}
{{- "valkey-password" -}}
{{- end -}}
{{- end -}}

{{- define "valkey-cluster.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "valkey-cluster.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* ------------------------------------------------------------------------
     Labels
   ------------------------------------------------------------------------ */}}

{{- define "valkey-cluster.labels" -}}
helm.sh/chart: {{ include "valkey-cluster.chart" . }}
{{ include "valkey-cluster.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- if .Values.partOf }}
app.kubernetes.io/part-of: {{ .Values.partOf }}
{{- end }}
{{- with .Values.commonLabels }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{- define "valkey-cluster.selectorLabels" -}}
app.kubernetes.io/name: {{ include "valkey-cluster.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "valkey-cluster.podLabels" -}}
{{ include "valkey-cluster.selectorLabels" . }}
app.kubernetes.io/component: node
{{- with .Values.podLabels }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{- define "valkey-cluster.commonAnnotations" -}}
{{- with .Values.commonAnnotations }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{/* ------------------------------------------------------------------------
     Image references
   ------------------------------------------------------------------------ */}}

{{- define "valkey-cluster.image" -}}
{{- $reg := .Values.image.registry -}}
{{- $repo := .Values.image.repository -}}
{{- $tag := .Values.image.tag -}}
{{- if $reg -}}
{{- printf "%s/%s:%s" $reg $repo $tag -}}
{{- else -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end -}}
{{- end -}}

{{- define "valkey-cluster.jobImage" -}}
{{- $reg := default .Values.image.registry .Values.jobs.image.registry -}}
{{- $repo := default .Values.image.repository .Values.jobs.image.repository -}}
{{- $tag := default .Values.image.tag .Values.jobs.image.tag -}}
{{- if $reg -}}
{{- printf "%s/%s:%s" $reg $repo $tag -}}
{{- else -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end -}}
{{- end -}}

{{- define "valkey-cluster.metricsImage" -}}
{{- $reg := .Values.metrics.image.registry -}}
{{- $repo := .Values.metrics.image.repository -}}
{{- $tag := .Values.metrics.image.tag -}}
{{- if $reg -}}
{{- printf "%s/%s:%s" $reg $repo $tag -}}
{{- else -}}
{{- printf "%s:%s" $repo $tag -}}
{{- end -}}
{{- end -}}

{{/* ------------------------------------------------------------------------
     Topology helpers
   ------------------------------------------------------------------------ */}}

{{- define "valkey-cluster.totalNodes" -}}
{{- $masters := int .Values.cluster.masters -}}
{{- $replicas := int .Values.cluster.replicasPerMaster -}}
{{- mul $masters (add1 $replicas) -}}
{{- end -}}

{{- define "valkey-cluster.podFQDN" -}}
{{- $idx := .idx -}}
{{- $root := .root -}}
{{- printf "%s-%d.%s.%s.svc.cluster.local" (include "valkey-cluster.fullname" $root) $idx (include "valkey-cluster.headlessServiceName" $root) $root.Release.Namespace -}}
{{- end -}}

{{/* Space-separated list of pod FQDNs at host:port for valkey-cli --cluster create. */}}
{{- define "valkey-cluster.allPodAddresses" -}}
{{- $root := . -}}
{{- $total := int (include "valkey-cluster.totalNodes" $root) -}}
{{- $port := $root.Values.cluster.clientPort | int -}}
{{- $svc := include "valkey-cluster.headlessServiceName" $root -}}
{{- $ns := $root.Release.Namespace -}}
{{- $name := include "valkey-cluster.fullname" $root -}}
{{- range $i, $_ := until $total -}}
{{- printf "%s-%d.%s.%s.svc.cluster.local:%d " $name $i $svc $ns $port -}}
{{- end -}}
{{- end -}}

{{/* ------------------------------------------------------------------------
     Anti-affinity preset
   ------------------------------------------------------------------------ */}}

{{- define "valkey-cluster.podAntiAffinity" -}}
{{- $preset := .Values.podAntiAffinityPreset -}}
{{- if eq $preset "hard" }}
podAntiAffinity:
  requiredDuringSchedulingIgnoredDuringExecution:
    - labelSelector:
        matchLabels:
          {{- include "valkey-cluster.selectorLabels" . | nindent 10 }}
      topologyKey: kubernetes.io/hostname
{{- else if eq $preset "soft" }}
podAntiAffinity:
  preferredDuringSchedulingIgnoredDuringExecution:
    - weight: 100
      podAffinityTerm:
        labelSelector:
          matchLabels:
            {{- include "valkey-cluster.selectorLabels" . | nindent 12 }}
        topologyKey: kubernetes.io/hostname
{{- end -}}
{{- end -}}
