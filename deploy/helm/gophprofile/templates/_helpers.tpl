{{/*
Базовое имя релиза. fullnameOverride задаёт его целиком, nameOverride — только
часть от чарта. Обрезка до 63 символов не косметика: имя попадает в лейблы,
а там это жёсткий предел Kubernetes.
*/}}
{{- define "gophprofile.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "gophprofile.fullname" -}}
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
Лейблы всего релиза. Меняются при обновлении версии, поэтому в селекторы
не годятся — селектор Deployment неизменяем после создания.
*/}}
{{- define "gophprofile.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "gophprofile.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: gophprofile
{{- end -}}

{{- define "gophprofile.selectorLabels" -}}
app.kubernetes.io/name: {{ include "gophprofile.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Лейблы одного компонента. Принимают словарь {root, component}: в шаблонах
Helm у include один аргумент, и корневой контекст приходится передавать явно.
*/}}
{{- define "gophprofile.componentLabels" -}}
{{ include "gophprofile.labels" .root }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{- define "gophprofile.componentSelectorLabels" -}}
{{ include "gophprofile.selectorLabels" .root }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{- define "gophprofile.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "gophprofile.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "gophprofile.configMapName" -}}
{{- printf "%s-config" (include "gophprofile.fullname" .) -}}
{{- end -}}

{{/*
Имя Secret. existingSecret позволяет не хранить пароли в values: настоящие
секреты приезжают из хранилища снаружи чарта, а чарт только ссылается.
*/}}
{{- define "gophprofile.secretName" -}}
{{- if .Values.secrets.existingSecret -}}
{{- .Values.secrets.existingSecret -}}
{{- else -}}
{{- printf "%s-secrets" (include "gophprofile.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "gophprofile.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}

{{/*
Хост базы. Встроенная зависимость адресуется по своему Service, внешняя —
по значению из values. Одно место принятия решения на весь чарт: иначе
переключение на внешнюю базу пришлось бы помнить в пяти шаблонах сразу.
*/}}
{{- define "gophprofile.dbHost" -}}
{{- if .Values.postgresql.enabled -}}
{{- printf "%s-postgresql" (include "gophprofile.fullname" .) -}}
{{- else -}}
{{- required "database.host обязателен, когда postgresql.enabled=false" .Values.database.host -}}
{{- end -}}
{{- end -}}

{{- define "gophprofile.s3Endpoint" -}}
{{- if .Values.minio.enabled -}}
{{- printf "%s-minio:9000" (include "gophprofile.fullname" .) -}}
{{- else -}}
{{- required "s3.endpoint обязателен, когда minio.enabled=false" .Values.s3.endpoint -}}
{{- end -}}
{{- end -}}

{{- define "gophprofile.rabbitmqHost" -}}
{{- printf "%s-rabbitmq" (include "gophprofile.fullname" .) -}}
{{- end -}}

{{/*
URL брокера. Он содержит пароль целиком, поэтому живёт в Secret, а не в
ConfigMap. Для встроенного RabbitMQ собирается здесь, для внешнего берётся
из values как есть.
*/}}
{{- define "gophprofile.rabbitmqURL" -}}
{{- if .Values.rabbitmq.enabled -}}
{{- printf "amqp://%s:%s@%s:5672/" .Values.rabbitmq.auth.username .Values.rabbitmq.auth.password (include "gophprofile.rabbitmqHost" .) -}}
{{- else -}}
{{- required "rabbitmq.url обязателен, когда rabbitmq.enabled=false" .Values.rabbitmq.url -}}
{{- end -}}
{{- end -}}

{{/*
Контекст безопасности пода и контейнера. Один набор на все рабочие нагрузки
чарта — включая джоб миграций, который иначе тихо остался бы привилегированным.
*/}}
{{- define "gophprofile.podSecurityContext" -}}
runAsNonRoot: true
runAsUser: {{ .Values.securityContext.runAsUser }}
runAsGroup: {{ .Values.securityContext.runAsGroup }}
fsGroup: {{ .Values.securityContext.runAsGroup }}
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{- define "gophprofile.containerSecurityContext" -}}
allowPrivilegeEscalation: false
privileged: false
readOnlyRootFilesystem: {{ .Values.securityContext.readOnlyRootFilesystem }}
runAsNonRoot: true
runAsUser: {{ .Values.securityContext.runAsUser }}
capabilities:
  drop:
    - ALL
{{- end -}}

{{/*
Переменные окружения с секретами. Явные ссылки, а не envFrom: так видно,
какой ключ куда идёт, и опечатка в имени ключа ломает старт пода, а не
тихо оставляет переменную пустой.
*/}}
{{- define "gophprofile.secretEnv" -}}
- name: DB_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ include "gophprofile.secretName" . }}
      key: db-password
- name: S3_ACCESS_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "gophprofile.secretName" . }}
      key: s3-access-key
- name: S3_SECRET_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "gophprofile.secretName" . }}
      key: s3-secret-key
- name: RABBITMQ_URL
  valueFrom:
    secretKeyRef:
      name: {{ include "gophprofile.secretName" . }}
      key: rabbitmq-url
{{- end -}}

{{/*
Том под временные файлы. readOnlyRootFilesystem обязывает: разбор multipart
сбрасывает тело запроса больше порога памяти в os.TempDir(), и без этого тома
любая крупная загрузка падала бы на записи во временный файл.
*/}}
{{- define "gophprofile.tmpVolume" -}}
- name: tmp
  emptyDir:
    sizeLimit: {{ .Values.tmpVolumeSize }}
{{- end -}}

{{- define "gophprofile.tmpVolumeMount" -}}
- name: tmp
  mountPath: /tmp
{{- end -}}
