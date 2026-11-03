{{- define "javo.image" -}}
{{- if .Values.image.digest -}}
{{ .Values.image.repository }}@{{ .Values.image.digest }}
{{- else -}}
{{ .Values.image.repository }}:{{ .Values.image.tag }}
{{- end -}}
{{- end -}}

{{- define "javo.labels" -}}
app.kubernetes.io/name: javo
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}

{{- define "javo.selector" -}}
app.kubernetes.io/name: javo
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{/* The API group; matches model.Group in internal/model. */}}
{{- define "javo.group" -}}
javo.nebari.dev
{{- end -}}

{{/* A label or annotation key under the API group, e.g. include "javo.groupKey" "role". Matches the keys in internal/model/labels.go. */}}
{{- define "javo.groupKey" -}}
{{ include "javo.group" . }}/{{ . }}
{{- end -}}

{{/* The sizes file (templates/sizes.yaml) both components read: the folder it is mounted at, and the --sizes-file flag. */}}
{{- define "javo.sizesDir" -}}
/etc/javo-sizes
{{- end -}}

{{- define "javo.sizesArg" -}}
--sizes-file={{ include "javo.sizesDir" . }}/sizes.yaml
{{- end -}}

{{/* Pod annotation that changes with the sizes, so editing them restarts the pods that read them at start. */}}
{{- define "javo.sizesChecksum" -}}
checksum/sizes: {{ include (print .Template.BasePath "/sizes.yaml") . | sha256sum }}
{{- end -}}

{{/* Annotation that rolls a Deployment when the platform config changes. */}}
{{- define "javo.configChecksum" -}}
checksum/config: {{ include (print $.Template.BasePath "/config.yaml") . | sha256sum }}
{{- end -}}

{{/* Renders a value that may be a template ({{ .Values.global.domain }},
{{ .Release.Namespace }}), so a parent chart can derive it: (list $ value). */}}
{{- define "javo.tpl" -}}
{{- $v := index . 1 -}}
{{- if $v }}{{ tpl (toString $v) (index . 0) }}{{ end -}}
{{- end -}}

{{/* config.urls as JSON, its hosts rendered as templates; uiHost defaults to
ingress.host. */}}
{{- define "javo.urls" -}}
{{- $urls := deepCopy (default (dict) .Values.config.urls) -}}
{{- range $k := list "uiHost" "appsHost" -}}
{{- with include "javo.tpl" (list $ (index $urls $k)) }}{{ $_ := set $urls $k . }}{{ end -}}
{{- end -}}
{{- if and .Values.ingress.host (not $urls.uiHost) -}}
{{- $_ := set $urls "uiHost" .Values.ingress.host -}}
{{- end -}}
{{- toJson $urls -}}
{{- end -}}

{{/* The host browsers reach the gateway at: publicHost, else the UI host. */}}
{{- define "javo.publicHost" -}}
{{- .Values.publicHost | default ((include "javo.urls" . | fromJson).uiHost | default "") -}}
{{- end -}}

{{/* The namespace mode: shared, per-user or per-team. */}}
{{- define "javo.namespaceMode" -}}
{{ (default (dict) .Values.config.namespaces).mode | default "shared" }}
{{- end -}}

{{/* The labels of the gateway pods as key=value pairs, for the controller's NetworkPolicies. */}}
{{- define "javo.gatewayPodLabels" -}}
{{- $pairs := list -}}
{{- range $k, $v := include "javo.selector" (dict "root" . "component" "gateway") | fromYaml -}}
{{- $pairs = append $pairs (printf "%s=%s" $k $v) -}}
{{- end -}}
{{ join "," $pairs }}
{{- end -}}

{{/* The ClusterRole of what one component (list $ "javo-gateway") may write
where apps live; the controller binds it in each managed namespace. */}}
{{- define "javo.appRoleName" -}}
{{- $ := index . 0 -}}
{{ $.Release.Name }}-{{ $.Release.Namespace }}-{{ index . 1 }}-apps
{{- end -}}

{{/* Where the gateway mounts the OIDC client secret (gateway.oidcClientSecret). */}}
{{- define "javo.oidcSecretDir" -}}
/etc/javo-oidc
{{- end -}}

{{/* The database engine, checked: sqlite (one pod runs both components) or postgres (one Deployment each). */}}
{{- define "javo.engine" -}}
{{- $e := (default (dict) .Values.database).engine | default "sqlite" -}}
{{- if not (has $e (list "sqlite" "postgres")) -}}
{{- fail (printf "database.engine must be sqlite or postgres, not %q" $e) -}}
{{- end -}}
{{- $e -}}
{{- end -}}

{{/* Fails when SQLite is asked to run on more than one pod: its file has one writer. */}}
{{- define "javo.checkReplicas" -}}
{{- if eq (include "javo.engine" .) "sqlite" -}}
{{- if or (gt (int .Values.gateway.replicas) 1) (gt (int .Values.controller.replicas) 1) -}}
{{- fail "database.engine=sqlite runs one pod: set gateway.replicas and controller.replicas to 1, or use database.engine=postgres for more replicas" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* The service account a component runs as, (list $ "javo-gateway"). With SQLite one pod runs both, as the controller's account. */}}
{{- define "javo.account" -}}
{{- if eq (include "javo.engine" (index . 0)) "sqlite" -}}javo-controller{{- else -}}{{ index . 1 }}{{- end -}}
{{- end -}}

{{/* Where the pod mounts the SQLite volume and the Postgres connection string. */}}
{{- define "javo.dataDir" -}}
/var/lib/javo
{{- end -}}

{{- define "javo.dsnDir" -}}
/etc/javo-db
{{- end -}}

{{/* Where the built-in template files are mounted, and the flag that installs them. */}}
{{- define "javo.templatesDir" -}}
/etc/javo-templates
{{- end -}}

{{/* Database flags of every javo process. */}}
{{- define "javo.databaseArgs" -}}
{{- if eq (include "javo.engine" .) "sqlite" -}}
- --database-engine=sqlite
- --database-path={{ include "javo.dataDir" . }}/javo.db
{{- else -}}
{{- if not .Values.database.postgres.dsnSecret -}}
{{- fail "database.postgres.dsnSecret is required with database.engine=postgres: the name of a Secret whose key dsn holds the connection string" -}}
{{- end }}
- --database-engine=postgres
- --database-dsn-file={{ include "javo.dsnDir" . }}/dsn
{{- end -}}
{{- end -}}

{{/* Volume mounts and volumes for the database. */}}
{{/*
The Secret holding the key-encryption key of people's secrets: the
controller's only, read through the API at start. The chart renders no key:
the controller makes javo-secrets-key on its first start, unless
secrets.existingKeySecret names one made elsewhere.
*/}}
{{- define "javo.secretsKeySecret" -}}{{ .Values.secrets.existingKeySecret | default "javo-secrets-key" }}{{- end -}}

{{/*
The Secrets of the gateway's sessions key (the gateway's only) and of the
secrets' fingerprint key (both components'), read through the API at start.
The chart renders neither: the components make them on their first start
(the --...-create flags) unless secrets.existingSessionsKeySecret or
secrets.existingFingerprintKeySecret names one made elsewhere.
*/}}
{{- define "javo.sessionsKeySecret" -}}{{ .Values.secrets.existingSessionsKeySecret | default "javo-sessions-key" }}{{- end -}}
{{- define "javo.fingerprintKeySecret" -}}{{ .Values.secrets.existingFingerprintKeySecret | default "javo-secret-fingerprint-key" }}{{- end -}}

{{- define "javo.databaseMounts" -}}
{{- if eq (include "javo.engine" .) "sqlite" -}}
- {name: data, mountPath: {{ include "javo.dataDir" . }}}
{{- else -}}
- {name: database, mountPath: {{ include "javo.dsnDir" . }}, readOnly: true}
{{- end -}}
{{- end -}}

{{- define "javo.databaseVolumes" -}}
{{- if eq (include "javo.engine" .) "sqlite" -}}
- name: data
  persistentVolumeClaim: {claimName: javo-data}
{{- else -}}
- name: database
  secret:
    secretName: {{ .Values.database.postgres.dsnSecret }}
    items: [{key: dsn, path: dsn}]
{{- end -}}
{{- end -}}

{{/* Scheduled backups: "true" when database.backup.schedule is set (keep checked), else empty. */}}
{{- define "javo.backupEnabled" -}}
{{- $b := (default (dict) .Values.database).backup | default (dict) -}}
{{- if $b.schedule -}}
{{- if lt (int $b.keep) 1 -}}
{{- fail "database.backup.keep must be at least 1" -}}
{{- end -}}
true
{{- end -}}
{{- end -}}

{{/* "true" when the controller makes the backup claim javo-backups: Postgres
backups with no bucket. The claim's only user is the nightly CronJob, so on
WaitForFirstConsumer storage it stays Pending until the first backup and
`helm --wait` would time out on it if the release rendered it. With a bucket
the dumps go there and the CronJob needs only scratch space; SQLite's server
mounts the claim, which binds at install, so the chart renders it. */}}
{{- define "javo.backupClaimByController" -}}
{{- if and (eq (include "javo.engine" .) "postgres") (include "javo.backupEnabled" .) (not (include "javo.bucketEnabled" .)) -}}
true
{{- end -}}
{{- end -}}

{{/* Where the SQLite server mounts the backup claim. */}}
{{- define "javo.backupDir" -}}
/var/lib/javo-backups
{{- end -}}

{{/* The SQLite server takes its own backups: flags, mount and volume. */}}
{{- define "javo.backupArgs" -}}
{{- if and (eq (include "javo.engine" .) "sqlite") (include "javo.backupEnabled" .) -}}
- --backup-schedule={{ .Values.database.backup.schedule }}
- --backup-dir={{ include "javo.backupDir" . }}
- --backup-keep={{ .Values.database.backup.keep }}
{{- end -}}
{{- end -}}

{{- define "javo.backupMounts" -}}
{{- if and (eq (include "javo.engine" .) "sqlite") (include "javo.backupEnabled" .) -}}
- {name: backups, mountPath: {{ include "javo.backupDir" . }}}
{{- end -}}
{{- end -}}

{{- define "javo.backupVolumes" -}}
{{- if and (eq (include "javo.engine" .) "sqlite") (include "javo.backupEnabled" .) -}}
- name: backups
  persistentVolumeClaim: {claimName: javo-backups}
{{- end -}}
{{- end -}}

{{/* Flags both components take. */}}
{{- define "javo.commonArgs" -}}
- --workspace-namespace={{ .Values.workspaceNamespace }}
- {{ include "javo.sizesArg" . }}
- --secret-fingerprint-key-secret={{ include "javo.fingerprintKeySecret" . }}
{{- if not .Values.secrets.existingFingerprintKeySecret }}
- --secret-fingerprint-key-create=true
{{- end }}
{{- end -}}

{{/* The gateway's own flags. */}}
{{- define "javo.gatewayArgs" -}}
- --listen=:{{ .Values.gateway.port }}
- --ui-dir=/ui
- --secure-cookies={{ .Values.gateway.secureCookies }}
- --files-image={{ .Values.files.image }}
- --files-idle-after={{ .Values.files.idleAfter }}
- --templates-dir={{ include "javo.templatesDir" . }}
- --sessions-key-secret={{ include "javo.sessionsKeySecret" . }}
{{- if not .Values.secrets.existingSessionsKeySecret }}
- --sessions-key-create=true
{{- end }}
{{- with .Values.gateway.trustedProxies }}
- --trusted-proxies={{ join "," . }}
{{- end }}
{{- end -}}

{{/* The controller's own flags. */}}
{{- define "javo.controllerArgs" -}}
- --network-policy={{ .Values.networkPolicy.enabled }}
{{- with .Values.networkPolicy.egress }}
- --egress-deny-cidrs={{ join "," .denyCIDRs }}
{{- with .allowCIDRs }}
- --egress-allow-cidrs={{ join "," . }}
{{- end }}
{{- end }}
- --gateway-pod-labels={{ include "javo.gatewayPodLabels" . }}
{{- if ne (include "javo.namespaceMode" .) "shared" }}
{{- range $sa := list "javo-gateway" "javo-controller" }}
- --app-role-binding={{ include "javo.appRoleName" (list $ $sa) }}={{ $.Release.Namespace }}/{{ include "javo.account" (list $ $sa) }}
{{- end }}
{{- end }}
- --metrics-bind-address=:{{ .Values.controller.metricsPort }}
- --health-probe-bind-address=:{{ .Values.controller.probePort }}
- --runner-image={{ include "javo.image" . }}
{{- range .Values.imagePullSecrets }}
- --image-pull-secret={{ .name }}
{{- end }}
- --secrets-key-secret={{ include "javo.secretsKeySecret" . }}
{{- if not .Values.secrets.existingKeySecret }}
- --secrets-key-create=true
{{- end }}
- --netprobe-control-url=http://javo-gateway.{{ .Release.Namespace }}.svc:{{ .Values.gateway.service.port }}/healthz
- --netprobe-interval={{ .Values.networkPolicy.probeInterval }}
{{- if .Values.notifications.enabled }}
- --notifications-namespace={{ .Release.Namespace }}
- --notifications-config-map={{ .Values.notifications.configMapName }}
- --notifications-secret={{ .Values.notifications.secretName }}
{{- end }}
{{- with include "javo.publicHost" . }}
- --public-host={{ . }}
{{- end }}
{{- if include "javo.backupClaimByController" . }}
- --backup-claim=javo-backups
- --backup-claim-size={{ .Values.database.backup.size }}
{{- with .Values.database.backup.storageClass }}
- --backup-claim-storage-class={{ . }}
{{- end }}
{{- end }}
{{- end -}}

{{/* Pod annotations that roll the pod when what it reads at start changes. */}}
{{- define "javo.rollAnnotations" -}}
{{ include "javo.sizesChecksum" . }}
{{ include "javo.configChecksum" . }}
checksum/templates: {{ include (print .Template.BasePath "/builtin-templates.yaml") . | sha256sum }}
checksum/branding: {{ include "javo.brandingFiles" . | sha256sum }}
{{- end -}}

{{/* The init container that migrates the database. */}}
{{- define "javo.upgradeContainer" -}}
- name: upgrade
  image: {{ include "javo.image" . | quote }}
  imagePullPolicy: {{ .Values.image.pullPolicy }}
  args:
    - db
    - upgrade
    {{- include "javo.databaseArgs" . | nindent 4 }}
  securityContext:
    {{- toYaml .Values.securityContext | nindent 4 }}
  volumeMounts:
    - {name: tmp, mountPath: /tmp}
    {{- include "javo.databaseMounts" . | nindent 4 }}
{{- end -}}

{{/* The model API key for the AI features, from the Secret in
ai.apiKeySecret, only when config.ai.enabled. Only the gateway gets it. */}}
{{- define "javo.aiEnv" -}}
{{- $s := .Values.ai.apiKeySecret -}}
{{- if and (default (dict) .Values.config.ai).enabled $s.name }}
- name: JAVO_AI_API_KEY
  valueFrom:
    secretKeyRef:
      name: {{ $s.name | quote }}
      key: {{ index $s "key" | quote }}
{{- end }}
{{- end -}}
{{/* The S3-compatible backup bucket (backupTarget): its key and the backup
password, mounted where config.backup.s3 says. */}}
{{- define "javo.bucketEnabled" -}}
{{- if .Values.backupTarget.enabled -}}
{{- $t := .Values.backupTarget -}}
{{- if not $t.endpoint }}{{ fail "backupTarget.endpoint is required with backupTarget.enabled" }}{{ end -}}
{{- if not $t.bucket }}{{ fail "backupTarget.bucket is required with backupTarget.enabled" }}{{ end -}}
{{- if not $t.existingSecret }}{{ fail "backupTarget.existingSecret is required with backupTarget.enabled: a Secret with the bucket's access key (keys AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY by default)" }}{{ end -}}
true
{{- end -}}
{{- end -}}
{{- define "javo.bucketPasswordSecret" -}}{{ .Values.backupTarget.password.existingSecret | default "javo-backup-password" }}{{- end -}}
{{- define "javo.bucketMounts" -}}
{{- if include "javo.bucketEnabled" . -}}
- {name: bucket-key, mountPath: /etc/javo-backup/s3, readOnly: true}
- {name: bucket-password, mountPath: /etc/javo-backup/password, readOnly: true}
{{- if .Values.backupTarget.tls.caSecret }}
- {name: bucket-ca, mountPath: /etc/javo-backup/ca, readOnly: true}
{{- end }}
{{- end -}}
{{- end -}}
{{- define "javo.bucketVolumes" -}}
{{- if include "javo.bucketEnabled" . -}}
- name: bucket-key
  secret:
    secretName: {{ .Values.backupTarget.existingSecret }}
    defaultMode: 0400
    items:
      - {key: {{ .Values.backupTarget.keys.accessKeyID }}, path: access-key-id}
      - {key: {{ .Values.backupTarget.keys.secretAccessKey }}, path: secret-access-key}
- name: bucket-password
  secret:
    secretName: {{ include "javo.bucketPasswordSecret" . }}
    defaultMode: 0400
    items: [{key: password, path: password}]
{{- with .Values.backupTarget.tls.caSecret }}
- name: bucket-ca
  secret:
    secretName: {{ . }}
    items: [{key: ca.crt, path: ca.crt}]
{{- end }}
{{- end -}}
{{- end -}}

{{- /* The image's pull secrets on a pod spec; nothing when there are none. */ -}}
{{- define "javo.imagePullSecrets" -}}
{{- with .Values.imagePullSecrets -}}
imagePullSecrets:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}

{{/* Where the gateway reads the branding images given as file contents. */}}
{{- define "javo.brandingDir" -}}
/etc/javo-branding
{{- end -}}

{{/* The branding images as a list of (name, value, key) dicts; fails when one has more than one source. */}}
{{- define "javo.brandingImages" -}}
{{- $b := .Values.branding -}}
{{- $out := list -}}
{{- range $i := list (dict "name" "logo-light" "key" "branding.logo.light" "v" $b.logo.light) (dict "name" "logo-dark" "key" "branding.logo.dark" "v" $b.logo.dark) (dict "name" "favicon" "key" "branding.favicon" "v" $b.favicon) -}}
{{- $v := default (dict) $i.v -}}
{{- $n := 0 -}}
{{- range $k := list "url" "svg" "base64" }}{{ if index $v $k }}{{ $n = add1 $n }}{{ end }}{{ end -}}
{{- if gt $n 1 }}{{ fail (printf "%s: set one of url, svg or base64" $i.key) }}{{ end -}}
{{- $out = append $out (dict "name" $i.name "v" $v) -}}
{{- end -}}
{{- toJson $out -}}
{{- end -}}

{{/* The branding images that are files, as ConfigMap binaryData; empty when none. */}}
{{- define "javo.brandingFiles" -}}
{{- range $i := include "javo.brandingImages" . | fromJsonArray -}}
{{- if $i.v.svg }}
{{ $i.name }}: {{ $i.v.svg | b64enc }}
{{- else if $i.v.base64 }}
{{ $i.name }}: {{ $i.v.base64 | nospace }}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* The config file's branding section. */}}
{{- define "javo.brandingConfig" -}}
{{- $root := . -}}
{{- $imgs := dict -}}
{{- range $i := include "javo.brandingImages" . | fromJsonArray -}}
{{- if $i.v.url -}}
{{- $_ := set $imgs $i.name (dict "url" $i.v.url) -}}
{{- else if or $i.v.svg $i.v.base64 -}}
{{- $_ := set $imgs $i.name (dict "file" (printf "%s/%s" (include "javo.brandingDir" $root) $i.name)) -}}
{{- else -}}
{{- $_ := set $imgs $i.name (dict) -}}
{{- end -}}
{{- end -}}
{{- $out := dict "logo" (dict "light" (get $imgs "logo-light") "dark" (get $imgs "logo-dark")) "favicon" (get $imgs "favicon") -}}
{{- with .Values.branding.name }}{{ $_ := set $out "name" . }}{{ end -}}
{{- with .Values.branding.accent }}{{ $_ := set $out "accent" . }}{{ end -}}
{{- toJson $out -}}
{{- end -}}

{{- define "javo.brandingMount" -}}
{{- if include "javo.brandingFiles" . -}}
- {name: branding, mountPath: {{ include "javo.brandingDir" . }}, readOnly: true}
{{- end -}}
{{- end -}}

{{- define "javo.brandingVolume" -}}
{{- if include "javo.brandingFiles" . -}}
- name: branding
  configMap: {name: javo-branding}
{{- end -}}
{{- end -}}
