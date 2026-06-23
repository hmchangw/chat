{{/*
Common label set applied to every resource.
*/}}
{{- define "migration.labels" -}}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/instance: {{ .Release.Name }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end }}

{{/*
Shared env block that every Job template includes.
Injects ConfigMap values + secretKeyRef values so we prove both paths work.
*/}}
{{- define "migration.commonEnv" -}}
envFrom:
  - configMapRef:
      name: {{ .Release.Name }}-config
env:
  - name: SOURCE_MONGO_URI
    valueFrom:
      secretKeyRef:
        name: {{ .Release.Name }}-secret
        key: SOURCE_MONGO_URI
  - name: TARGET_MONGO_URI
    valueFrom:
      secretKeyRef:
        name: {{ .Release.Name }}-secret
        key: TARGET_MONGO_URI
{{- end }}

{{/*
Cassandra env additions (Phase 3 only).
*/}}
{{- define "migration.cassandraEnv" -}}
  - name: CASSANDRA_HOSTS
    valueFrom:
      secretKeyRef:
        name: {{ .Release.Name }}-secret
        key: CASSANDRA_HOSTS
  - name: CASSANDRA_USER
    valueFrom:
      secretKeyRef:
        name: {{ .Release.Name }}-secret
        key: CASSANDRA_USER
  - name: CASSANDRA_PASSWORD
    valueFrom:
      secretKeyRef:
        name: {{ .Release.Name }}-secret
        key: CASSANDRA_PASSWORD
{{- end }}
