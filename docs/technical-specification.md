# Техническое задание: Сервис "Аватарница"

## Общая формулировка задания

Необходимо разработать микросервис "Аватарница" для управления аватарками пользователей с применением современных практик разработки на Go и DevOps. Сервис должен обеспечивать загрузку, хранение, получение и удаление аватарок пользователей через веб-интерфейс и REST API с соблюдением принципов безопасности, масштабируемости и наблюдаемости.

## Пример API

```
# Загрузка аватарки
POST /api/v1/avatars
Content-Type: multipart/form-data
Headers: X-User-ID: string

# Получение аватарки
GET /api/v1/avatars/{avatar_id}
GET /api/v1/users/{user_id}/avatar

# Удаление аватарки
DELETE /api/v1/avatars/{avatar_id}
DELETE /api/v1/users/{user_id}/avatar
Headers: X-User-ID: string

# Получение метаданных аватарки
GET /api/v1/avatars/{avatar_id}/metadata

# Список аватарок пользователя
GET /api/v1/users/{user_id}/avatars

# Проверка работоспособности
GET /health

# Веб-интерфейс
GET /web/upload - форма загрузки
POST /web/upload - обработка загрузки
GET /web/gallery/{user_id} - галерея аватарок
```

## План выполнения задания

### Этап 1: Разработка приложения

#### 1.1 Архитектура и инфраструктура

**Компоненты системы:**
- HTTP сервер с REST API и веб-интерфейсом
- Сервис обработки изображений
- Брокер сообщений (RabbitMQ или Kafka на выбор)
- База данных PostgreSQL для метаданных
- S3-совместимое хранилище для файлов
- Worker для асинхронной обработки

**Возможная структура проекта:**
```
avatars-service/
├── cmd/
│   ├── server/
│   └── worker/
├── internal/
│   ├── api/
│   ├── config/
│   ├── domain/
│   ├── handlers/
│   ├── repository/
│   ├── services/
│   └── worker/
├── pkg/
├── web/
├── migrations/
├── docker/
├── k8s/
└── tests/
```

#### 1.2 Технические требования

**Основной стек:**
- Go 1.21+
- Echo/Chi для HTTP роутинга
- PostgreSQL для метаданных
- MinIO/AWS S3 для хранения файлов
- RabbitMQ или Kafka для очередей
- Docker и Docker Compose

**Брокер сообщений (на выбор):**
- **RabbitMQ**: используйте exchange типа `direct` или `topic`
- **Kafka**: создайте топики для обработки изображений

**База данных:**
- **PostgreSQL**: таблицы для метаданных, индексы, миграции

#### 1.3 Функциональные требования

**API эндпоинты:**
1. `POST /api/v1/avatars` - загрузка аватарки
   - Валидация формата (JPEG, PNG, WebP) - опционально
   - Ограничение размера (до 10MB)
   - Создание миниатюр (100x100, 300x300)
   - Асинхронная обработка через брокер

```json
POST /api/v1/avatars
Headers:
  X-User-ID: string (required)
  Content-Type: multipart/form-data
Body:
  file: binary (required, max 10MB)
Response 201:
  {
    "id": "uuid",
    "user_id": "string",
    "url": "string",
    "status": "processing",
    "created_at": "2024-01-01T00:00:00Z"
  }
Response 400:
  {
    "error": "Invalid file format",
    "details": "Supported formats: jpeg, png, webp"
  }
Response 413:
  {
    "error": "File too large",
    "max_size": 10485760
  }
```

2. `GET /api/v1/avatars/{id}` - получение аватарки
   - Поддержка query параметров (размер, формат) - опционально
   - Кэширование заголовков - опционально
   - Content-Type в зависимости от формата

```
GET /api/v1/avatars/{avatar_id}?size=300x300&format=webp
Parameters:
  size: string (optional, values: "100x100", "300x300", "original")
  format: string (optional, values: "jpeg", "png", "webp")
Response 200:
  Binary image data
  Headers:
    Content-Type: image/jpeg|png|webp
    Cache-Control: max-age=86400
    ETag: "hash"
Response 404:
  {
    "error": "Avatar not found"
  }
```

Получение метаданных
```
GET /api/v1/avatars/{avatar_id}/metadata
Response 200:
  {
    "id": "uuid",
    "user_id": "string",
    "file_name": "avatar.jpg",
    "mime_type": "image/jpeg",
    "size": 1024000,
    "dimensions": {
      "width": 1920,
      "height": 1080
    },
    "thumbnails": [
      {"size": "100x100", "url": "..."},
      {"size": "300x300", "url": "..."}
    ],
    "created_at": "2024-01-01T00:00:00Z",
    "updated_at": "2024-01-01T00:00:00Z"
  }
```



3. `DELETE /api/v1/avatars/{id}` - удаление аватарки
   - Мягкое удаление в БД
   - Асинхронное удаление из S3

```json
DELETE /api/v1/avatars/{avatar_id}
Headers:
  X-User-ID: string (required)
Response 204: No Content
Response 403:
  {
    "error": "Forbidden",
    "details": "You can only delete your own avatars"
  }
```

4. `GET /health` - healthcheck
   - Проверка БД, S3, брокера
   - JSON ответ со статусами компонентов


**Веб-интерфейс:**
- Форма загрузки (с drag&drop - опционально)
- Превью изображения
- Прогресс загрузки - опционально
- Галерея аватарок пользователя

#### 1.4 Обработка сообщений

**События для брокера:**
```go
type AvatarUploadEvent struct {
    AvatarID   string `json:"avatar_id"`
    UserID     string `json:"user_id"`
    S3Key      string `json:"s3_key"`
}

type AvatarProcessEvent struct {
    AvatarID   string            `json:"avatar_id"`
    Operations []ProcessingOp    `json:"operations"`
}

type AvatarDeleteEvent struct {
    AvatarID   string    `json:"avatar_id"`
    S3Keys     []string  `json:"s3_keys"`
}
```

**Идемпотентность:**
- Используйте уникальные идентификаторы сообщений
- Проверяйте статус обработки перед выполнением операций
- Реализуйте retry с экспоненциальным backoff

Примеры:
```go
// Пример отправки события после загрузки
func (s *AvatarService) PublishUploadEvent(avatarID, userID, s3Key string) error {
    event := AvatarUploadEvent{
        AvatarID: avatarID,
        UserID:   userID,
        S3Key:    s3Key,
    }
    
    // Для RabbitMQ
    return s.publisher.Publish(
        "avatars.exchange",     // exchange
        "avatar.uploaded",       // routing key
        event,
    )
    
    // Для Kafka
    return s.producer.Send(&sarama.ProducerMessage{
        Topic: "avatar-events",
        Key:   sarama.StringEncoder(avatarID),
        Value: sarama.JSONEncoder(event),
    })
}

// Пример обработки события в worker
func (w *Worker) HandleUploadEvent(event AvatarUploadEvent) error {
    // Получаем метаданные из БД
    avatar, err := w.repo.GetAvatar(event.AvatarID)
    if err != nil {
        return err
    }
    
    // Загружаем оригинал из S3
    image, err := w.s3.Download(event.S3Key)
    if err != nil {
        return err
    }
    
    // Создаем миниатюры
    thumbnails := []struct{
        size string
        data []byte
    }{
        {"100x100", w.resizer.Resize(image, 100, 100)},
        {"300x300", w.resizer.Resize(image, 300, 300)},
    }
    
    // Сохраняем миниатюры в S3
    for _, thumb := range thumbnails {
        key := fmt.Sprintf("thumbnails/%s/%s.jpg", event.AvatarID, thumb.size)
        if err := w.s3.Upload(key, thumb.data); err != nil {
            return err
        }
    }
    
    // Обновляем статус в БД
    return w.repo.UpdateProcessingStatus(event.AvatarID, "completed")
}
```

#### 1.5 Модель данных

**PostgreSQL схема - для примера:**
```sql
CREATE TABLE avatars (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id VARCHAR(255) NOT NULL,
    file_name VARCHAR(255) NOT NULL,
    mime_type VARCHAR(100) NOT NULL,
    size_bytes BIGINT NOT NULL,
    s3_key VARCHAR(500) NOT NULL,
    thumbnail_s3_keys JSONB,
    upload_status VARCHAR(50) DEFAULT 'uploading',
    processing_status VARCHAR(50) DEFAULT 'pending',
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    deleted_at TIMESTAMP WITH TIME ZONE
);

CREATE INDEX idx_avatars_user_id ON avatars(user_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_avatars_status ON avatars(upload_status, processing_status);
```

#### 1.6 Безопасность

- Валидация MIME-типов и magic bytes
- Ограничение размера файлов
- Rate limiting для API - опционально
- CORS настройки - опционально
  CORS (Cross-Origin Resource Sharing) - это механизм безопасности браузеров, который контролирует доступ веб-страниц к ресурсам с других доменов, портов или протоколов.
- Валидация User-ID из заголовков

#### 1.7 Тестирование

**Unit тесты (покрытие >80%):**
- Обработчики HTTP
- Сервисные слои
- Репозитории
- Утилиты для работы с изображениями

**Интеграционные тесты:**
- Тесты API с testcontainers
- Тесты с брокером сообщений
- Тесты S3 операций
- End-to-end тесты веб-интерфейса

**Инструменты:**
- `go test` для unit тестов
- `testify/suite` для интеграционных тестов
- `testcontainers-go` для тест окружения
- `golangci-lint` для статического анализа

#### 1.8 Докеризация

**Dockerfile (multi-stage build):**
```dockerfile
# Build stage
FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -o worker ./cmd/worker

# Runtime stage
FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /root/
COPY --from=builder /app/server .
COPY --from=builder /app/worker .
COPY --from=builder /app/web ./web/
```

**Docker Compose для разработки:**
- Приложение (server + worker)
- PostgreSQL
- RabbitMQ или Kafka
- MinIO

### Этап 2: Обеспечение Observability

#### 2.1 OpenTelemetry интеграция

**Трейсинг:**
- Инструментирование HTTP запросов
- Трейсы для работы с БД
- Трейсы для S3 операций
- Трейсы для брокера сообщений
- Контекст propagation между сервисами

```go
import (
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/attribute"
    "go.opentelemetry.io/otel/trace"
)

func (s *AvatarService) UploadAvatar(ctx context.Context, req *UploadRequest) error {
    ctx, span := otel.Tracer("avatar-service").Start(ctx, "upload_avatar")
    defer span.End()
    
    span.SetAttributes(
        attribute.String("user_id", req.UserID),
        attribute.String("file_name", req.FileName),
        attribute.Int64("file_size", req.Size),
    )
    // ...
}
```

**Метрики:**
```go
var (
    uploadsTotal = promauto.NewCounterVec(
        prometheus.CounterOpts{
            Name: "avatars_uploads_total",
            Help: "Total number of avatar uploads",
        },
        []string{"status", "user_id"},
    )
    
    uploadDuration = promauto.NewHistogramVec(
        prometheus.HistogramOpts{
            Name: "avatars_upload_duration_seconds",
            Help: "Avatar upload duration",
        },
        []string{"status"},
    )
    
    storageUsage = promauto.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "avatars_storage_bytes",
            Help: "Total storage used by avatars",
        },
        []string{"user_id"},
    )
)
```

**Логирование:**
- Структурированные логи (JSON)
- Корреляция с trace ID
- Уровни логирования
- Использование slog

```go
import "log/slog"

logger := slog.With(
    "service", "avatar-service",
    "trace_id", trace.SpanFromContext(ctx).SpanContext().TraceID(),
)

logger.Info("uploading avatar",
    "user_id", userID,
    "file_size", fileSize,
    "mime_type", mimeType,
)
```

#### 2.2 Мониторинг стек

**Prometheus метрики:**
- HTTP метрики (requests, duration, errors)
- Business метрики (uploads, storage usage)
- Инфраструктурные метрики (DB connections, queue depth)

**Grafana дашборды:**
- Service overview
- Request rate, error rate, duration (RED metrics)
- Resource utilization
- Business KPIs

**ELK/OpenSearch:**
- Централизованное логирование
- Алерты на ошибки
- Log aggregation и поиск

**Jaeger:**
- Distributed tracing
- Performance анализ
- Dependency mapping

#### 2.3 Алертинг

**Prometheus Alertmanager правила:**
```yaml
groups:
- name: avatar-service
  rules:
  - alert: HighErrorRate
    expr: rate(avatars_uploads_total{status="error"}[5m]) / rate(avatars_uploads_total[5m]) > 0.1
    for: 5m
    labels:
      severity: warning
  
  - alert: HighResponseTime
    expr: histogram_quantile(0.95, avatars_upload_duration_seconds) > 5
    for: 2m
    labels:
      severity: critical
```

### Этап 3: Развертывание в Kubernetes

#### 3.1 Kubernetes манифесты (пример)

**Deployment:**
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: avatar-service
spec:
  replicas: 3
  selector:
    matchLabels:
      app: avatar-service
  template:
    metadata:
      labels:
        app: avatar-service
    spec:
      containers:
      - name: server
        image: avatar-service:latest
        ports:
        - containerPort: 8080
        env:
        - name: DATABASE_URL
          valueFrom:
            secretKeyRef:
              name: avatar-secrets
              key: database-url
        resources:
          requests:
            memory: "128Mi"
            cpu: "100m"
          limits:
            memory: "512Mi"
            cpu: "500m"
        livenessProbe:
          httpGet:
            path: /health
            port: 8080
          initialDelaySeconds: 30
        readinessProbe:
          httpGet:
            path: /health
            port: 8080
          initialDelaySeconds: 5
```

**HorizontalPodAutoscaler:**
```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: avatar-service-hpa
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: avatar-service
  minReplicas: 2
  maxReplicas: 10
  metrics:
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 70
  - type: Resource
    resource:
      name: memory
      target:
        type: Utilization
        averageUtilization: 80
```

#### 3.2 Конфигурация и секреты (пример)

**ConfigMap:**
```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: avatar-config
data:
  max_file_size: "10485760"
  allowed_mime_types: "image/jpeg,image/png,image/webp"
  s3_bucket: "avatars"
```

**Secrets:**
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: avatar-secrets
type: Opaque
data:
  database-url: <base64-encoded>
  s3-access-key: <base64-encoded>
  s3-secret-key: <base64-encoded>
  rabbitmq-url: <base64-encoded>
```

#### 3.3 Networking

**Service:**
```yaml
apiVersion: v1
kind: Service
metadata:
  name: avatar-service
spec:
  selector:
    app: avatar-service
  ports:
  - port: 80
    targetPort: 8080
  type: ClusterIP
```

**Ingress:**
```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: avatar-ingress
  annotations:
    nginx.ingress.kubernetes.io/proxy-body-size: "10m"
spec:
  rules:
  - host: avatars.example.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: avatar-service
            port:
              number: 80
```

#### 3.4 Мониторинг в Kubernetes

**ServiceMonitor для Prometheus:**
```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: avatar-service
spec:
  selector:
    matchLabels:
      app: avatar-service
  endpoints:
  - port: metrics
    interval: 30s
```

#### 3.5 Безопасность в Kubernetes

**NetworkPolicy:**
```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: avatar-service-netpol
spec:
  podSelector:
    matchLabels:
      app: avatar-service
  policyTypes:
  - Ingress
  - Egress
  ingress:
  - from:
    - namespaceSelector:
        matchLabels:
          name: ingress-nginx
  egress:
  - to:
    - namespaceSelector:
        matchLabels:
          name: database
```

**PodSecurityPolicy и RBAC:**
- Ограничения на уровне подов
- Service account с минимальными правами
- SecurityContext с non-root пользователем

#### 3.6 Helm Chart (опционально)

Создание Helm chart для удобного деплоя:
- Templates для всех ресурсов
- Values файлы для разных окружений
- Хуки для миграций БД

## Критерии приемки

### Этап 1: Разработка приложения

**Функциональность:**
- [ ] Все API эндпоинты реализованы и работают корректно
- [ ] Веб-интерфейс позволяет загружать, просматривать и удалять аватарки
- [ ] Асинхронная обработка через брокер сообщений работает
- [ ] Идемпотентность обработки сообщений обеспечена
- [ ] Файлы корректно сохраняются в S3, метаданные в БД

**Качество кода:**
- [ ] Покрытие unit тестами >80%
- [ ] Интеграционные тесты покрывают основные сценарии
- [ ] Код проходит линтинг без ошибок
- [ ] Архитектура следует принципам Clean Architecture
- [ ] Обработка ошибок реализована корректно

**Докеризация:**
- [ ] Multi-stage Dockerfile оптимизирован
- [ ] Docker Compose для локальной разработки работает
- [ ] Образы собираются и запускаются без ошибок

### Этап 2: Observability

**Трейсинг:**
- [ ] OpenTelemetry интегрирован во все критичные операции
- [ ] Трейсы корректно передаются между компонентами
- [ ] Jaeger показывает полную картину запросов

**Метрики:**
- [ ] Prometheus метрики собираются и экспортируются
- [ ] Grafana дашборды отображают ключевые метрики
- [ ] Business метрики корректно отслеживаются

**Логирование:**
- [ ] Структурированные логи с корреляцией
- [ ] ELK/OpenSearch успешно индексирует логи
- [ ] Поиск и фильтрация логов работает

**Алертинг:**
- [ ] Настроены алерты на критичные метрики
- [ ] Алерты срабатывают в тестовых сценариях

### Этап 3: Kubernetes

**Развертывание:**
- [ ] Все компоненты успешно деплоятся в Kubernetes
- [ ] Health checks работают корректно
- [ ] Сервис доступен через Ingress

**Масштабируемость:**
- [ ] HPA корректно масштабирует поды
- [ ] Load balancing работает между репликами
- [ ] Graceful shutdown реализован

**Мониторинг:**
- [ ] Метрики собираются в Kubernetes окружении
- [ ] Дашборды отображают состояние кластера
- [ ] ServiceMonitor корректно настроен

**Безопасность:**
- [ ] NetworkPolicy ограничивает трафик
- [ ] Секреты используются для конфиденциальных данных
- [ ] Non-root контейнеры используются

### Общие критерии

**Документация:**
- [ ] README с инструкциями по запуску
- [ ] API документация (OpenAPI/Swagger)
- [ ] Архитектурная диаграмма
- [ ] Описание мониторинга и алертов

**Production готовность:**
- [ ] Graceful shutdown
- [ ] Circuit breaker для внешних зависимостей
- [ ] Rate limiting
- [ ] Proper error handling
- [ ] Resource limits настроены

Каждый этап должен быть завершен с соблюдением всех критериев приемки перед переходом к следующему этапу. Финальная демонстрация должна показать полнофункциональный сервис, развернутый в Kubernetes с полным мониторингом и наблюдаемостью.