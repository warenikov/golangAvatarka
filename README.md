# GophProfile — сервис аватарок

[![Тесты](https://github.com/warenikov/golangAvatarka/actions/workflows/test.yml/badge.svg)](https://github.com/warenikov/golangAvatarka/actions/workflows/test.yml)
[![Линт](https://github.com/warenikov/golangAvatarka/actions/workflows/lint.yml/badge.svg)](https://github.com/warenikov/golangAvatarka/actions/workflows/lint.yml)
[![Безопасность](https://github.com/warenikov/golangAvatarka/actions/workflows/security.yml/badge.svg)](https://github.com/warenikov/golangAvatarka/actions/workflows/security.yml)

Микросервис на Go: пользователь загружает фото один раз, сторонние платформы
получают аватарку по HTTP в нужном размере. Выпускная работа курса
«Go-разработчик», Яндекс Практикум.

Миниатюры считаются асинхронно: загрузка отвечает сразу, а обрезка и
масштабирование уходят в очередь. Пока миниатюра не готова, по её адресу
отдаётся оригинал — клиенту не нужно опрашивать статус.

## Наблюдаемость

Сервис инструментирован сквозной трассировкой, метриками и структурными логами,
связанными между собой одним `trace_id`.

```bash
make up-obs     # приложение вместе со стеком наблюдаемости
```

| Инструмент | Адрес | Что показывает |
|---|---|---|
| Jaeger | http://127.0.0.1:16686 | трейсы запросов, включая работу воркера |
| Grafana | http://127.0.0.1:3000 | дашборд бизнес-показателей |
| Prometheus | http://127.0.0.1:9090 | метрики и правила алертов |
| OpenSearch Dashboards | http://127.0.0.1:5601 | поиск по логам |
| Alertmanager | http://127.0.0.1:9093 | сработавшие алерты |

**Трейс переживает брокер.** Загрузка аватарки — это один трейс из двух
процессов: HTTP-запрос, запись в хранилище и базу, публикация события, а внутри
неё — потребление воркером, декодирование, построение миниатюр и выгрузка.
Контекст едет в заголовках сообщения AMQP в формате W3C `traceparent`, том же,
что ходит по HTTP.

```
[server] POST /api/v1/avatars
  [server] avatar.upload → s3.put, query INSERT, publish avatar.uploaded
    [worker] consume avatar.uploaded → avatar.process
      [worker] image.decode, image.thumbnail ×2, s3.put ×2, query UPDATE
```

**Логи связаны с трейсами.** Каждая запись несёт `trace_id` и `span_id`:
идентификатор из лога открывается в Jaeger, а по нему же в OpenSearch находятся
записи обоих сервисов.

**Метрики предметной области**, а не только технические: загрузки по результату,
размеры файлов, длительность построения миниатюр, отставание очереди обработки,
события, ушедшие в очередь разбора.

В Kubernetes то же самое делает kube-prometheus-stack: как собираются метрики,
какие есть дашборды и алерты — в разделе [Мониторинг](#мониторинг).

Стек вынесен в профиль `obs` — обычная разработка (`make up`) его не поднимает
и не платит за него памятью.

## Возможности

- загрузка JPEG, PNG и WebP с проверкой по сигнатуре файла, а не по расширению;
- квадратные миниатюры 100×100 и 300×300, центральный кроп по короткой стороне;
- отдача с `ETag` и поддержкой `If-None-Match` — повторный запрос стоит 304;
- заглушка для пользователя без аватарки, чтобы фронту не приходилось обрабатывать 404;
- мягкое удаление: метаданные помечаются сразу, файлы убирает воркер;
- веб-интерфейс без JavaScript — загрузка и галерея обычными формами;
- ограничение частоты запросов, CORS по белому списку, заголовки против сниффинга;
- автоматические выключатели на Postgres, S3 и брокере: при отказе зависимости
  запросы сразу получают 503 с `Retry-After`, а не висят до таймаута;
- сквозная трассировка, метрики Prometheus и структурные логи с корреляцией;
- Helm-чарт с автомасштабированием, сетевыми политиками, хуком миграций
  и дашбордами Grafana — бизнес-показатели и состояние релиза в кластере;
- спецификация OpenAPI 3.1 и Swagger UI, вшитые в бинарь.

## Быстрый старт

Нужен только Docker.

```bash
git clone https://github.com/warenikov/golangAvatarka.git
cd golangAvatarka
make up-all
```

Поднимутся PostgreSQL, MinIO, RabbitMQ, сервис миграций, сервер и воркер.
Сервер ответит на http://127.0.0.1:8080.

> После `networksetup -setv6off` на macOS используйте `127.0.0.1`, а не `localhost`:
> он резолвится в `::1` и не отвечает.

Занят порт 8080? Задайте свой:

```bash
APP_EXPOSED_PORT=8090 make up-all
```

Проверить, что всё живо:

```bash
curl -s http://127.0.0.1:8080/health
```

```json
{"status":"ok","components":{"postgres":{"status":"ok","latency_ms":0},
"rabbitmq":{"status":"ok","latency_ms":0},"s3":{"status":"ok","latency_ms":1}},
"version":"dev","uptime_s":12}
```

## Проверка сквозного пути

```bash
# загрузка
curl -X POST -H "X-User-ID: user-1" -F "file=@avatar.png" \
  http://127.0.0.1:8080/api/v1/avatars

# метаданные: processing_status станет completed через доли секунды
curl http://127.0.0.1:8080/api/v1/avatars/<id>/metadata

# миниатюра
curl -o thumb.jpg "http://127.0.0.1:8080/api/v1/avatars/<id>?size=100x100"

# удаление
curl -X DELETE -H "X-User-ID: user-1" http://127.0.0.1:8080/api/v1/avatars/<id>
```

## Интерфейсы

| Адрес | Что это |
|---|---|
| http://127.0.0.1:8080/web/upload | форма загрузки, работает без JavaScript |
| http://127.0.0.1:8080/web/gallery | галерея пользователя с миниатюрами |
| http://127.0.0.1:8080/static/ | одностраничный фронтенд из шаблона курса, не изменён |
| http://127.0.0.1:8080/docs | Swagger UI с описанием API |
| http://127.0.0.1:9001 | консоль MinIO |
| http://127.0.0.1:15672 | панель RabbitMQ |

## API

| Метод | Путь | Назначение |
|---|---|---|
| `POST` | `/api/v1/avatars` | загрузить аватарку |
| `GET` | `/api/v1/avatars/{id}` | получить файл, `?size=100x100\|300x300\|original` |
| `GET` | `/api/v1/avatars/{id}/metadata` | метаданные и список готовых миниатюр |
| `DELETE` | `/api/v1/avatars/{id}` | удалить свою аватарку |
| `GET` | `/api/v1/users/{user_id}/avatar` | последняя аватарка или заглушка |
| `GET` | `/api/v1/users/{user_id}/avatars` | все аватарки пользователя |
| `DELETE` | `/api/v1/users/{user_id}/avatar` | удалить последнюю |
| `GET` | `/health` | состояние зависимостей (синоним `/readyz`) |
| `GET` | `/livez` | жив ли процесс |
| `GET` | `/readyz` | готов ли принимать трафик |
| `GET` | `/openapi.yaml` | спецификация OpenAPI 3.1 |
| `GET` | `/docs` | Swagger UI поверх этой спецификации |

Полное описание контракта — в [api/openapi.yaml](api/openapi.yaml). Спецификация
вшита в бинарь и отдаётся самим сервисом, а Swagger UI лежит в образе: страница
обязана открываться в закрытом контуре и не зависеть от чужого домена.

`/metrics` на публичном порту нет намеренно. Метрики отдаются на служебном
порту 8081 вместе с `/livez` и `/readyz`: в метках лежит внутреннее устройство
сервиса, и через Ingress ему наружу не место.

Из проверок вне лимита частоты работает только `/livez` — она не трогает
зависимостей и стоит одного сравнения в памяти. `/health` и `/readyz`
на публичном порту ограничены: каждый такой запрос — три обращения
к инфраструктуре, и без лимита это был бы самый дешёвый способ нагрузить
базу снаружи. Пробы оркестратора сюда не ходят, у них служебный порт.

Загрузка принимает файл в поле `file` или `image` — второе имя использует
фронтенд из шаблона курса. Пользователь передаётся заголовком `X-User-ID`.

Коды ответов:

| Код | Когда |
|---|---|
| `201` | аватарка принята, обработка поставлена в очередь |
| `204` | удалено |
| `304` | `If-None-Match` совпал с `ETag` |
| `400` | нет или некорректен `X-User-ID`, не изображение, неизвестный размер |
| `403` | попытка удалить чужую аватарку |
| `404` | аватарка не найдена или уже удалена |
| `413` | файл больше `APP_MAX_UPLOAD_BYTES` |
| `429` | превышен лимит частоты запросов |

Ошибки приходят единым форматом:

```json
{ "error": "Avatar not found", "details": "" }
```

> Аутентификации в спринте 1 нет: `X-User-ID` никем не подтверждается.
> Это осознанное ограничение объёма, а не недосмотр — на нём держится только
> разделение аватарок между пользователями, но не доступ к сервису.

## Развёртывание в Kubernetes

Конфигурация упакована в Helm-чарт. Плоские манифесты в
[deploy/k8s/rendered](deploy/k8s/rendered) генерируются из него командой
`make k8s-render` и коммитятся: их можно читать и применять без Helm, но править
бессмысленно — источник истины один.

### Что должно быть в кластере

Пустой кластер не подойдёт: без metrics-server HPA остаётся в состоянии
`unknown`, без CRD оператора Prometheus не применяется `ServiceMonitor`.

```bash
helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx
helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server/
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update

helm upgrade --install ingress-nginx ingress-nginx/ingress-nginx \
  -n ingress-nginx --create-namespace \
  --set controller.service.type=LoadBalancer --wait

# Флаг обязателен: у kubelet в k3s самоподписанный сертификат,
# и без него metrics-server не соберёт ни одной метрики.
helm upgrade --install metrics-server metrics-server/metrics-server \
  -n kube-system --set 'args={--kubelet-insecure-tls}' --wait

helm upgrade --install kube-prometheus-stack prometheus-community/kube-prometheus-stack \
  -n monitoring --create-namespace \
  --set prometheus.prometheusSpec.serviceMonitorSelectorNilUsesHelmValues=false \
  --set prometheus.prometheusSpec.ruleSelectorNilUsesHelmValues=false \
  --set grafana.sidecar.dashboards.searchNamespace=ALL --wait
```

### Установка

```bash
make image        # собрать образ gophprofile:dev
make k8s-deploy   # неймспейс + helm upgrade --install
make k8s-status
```

Неймспейс применяется отдельно и **до** Helm — так задумано. Helm пишет
состояние релиза в Secret внутри целевого неймспейса раньше, чем применяет
манифесты, поэтому чарт не может создать собственный неймспейс. А флаг
`--create-namespace` создал бы его мимо релиза, без лейблов Pod Security
Admission, и повесить их потом уже нечем.

### Проверка

```bash
IP=$(kubectl -n ingress-nginx get svc ingress-nginx-controller \
  -o jsonpath='{.status.loadBalancer.ingress[0].ip}')

curl -s -H "Host: avatars.local" "http://$IP/health"
curl -s -H "Host: avatars.local" -H "X-User-ID: user-1" \
  -F "file=@avatar.png" "http://$IP/api/v1/avatars"
```

Служебный порт наружу не выходит:

```bash
kubectl -n gophprofile port-forward svc/gp-gophprofile-server 8081:8081
curl -s localhost:8081/readyz
```

### Мониторинг

Метрики снимает Prometheus из kube-prometheus-stack. Статических целей нет:
чарт создаёт по `ServiceMonitor` на сервер и воркер, и оператор сам следит
за endpoints — новые реплики HPA попадают в сбор без перезапуска Prometheus.

- метрики берутся со служебного порта `8081` (`/metrics`), публичный `8080` для этого не используется;
- к каждой серии добавляются метки `pod` и `component` (`server` или `worker`):
  по ним видно отдельную реплику, а не одну кривую на весь сервис;
- интервал сбора — `monitoring.serviceMonitor.interval` в values, по умолчанию 30 секунд;
- состояние кластера — поды, реплики, HPA, рестарты, лимиты — приходит
  из kube-state-metrics, который ставится вместе со стеком.

```bash
# Grafana: логин admin, пароль — из секрета стека
kubectl -n monitoring get secret kube-prometheus-stack-grafana \
  -o jsonpath='{.data.admin-password}' | base64 -d; echo
kubectl -n monitoring port-forward svc/kube-prometheus-stack-grafana 3000:80

# Prometheus: цели, правила и сработавшие алерты
kubectl -n monitoring port-forward svc/kube-prometheus-stack-prometheus 9090:9090
```

В Prometheus на странице Status → Targets должны быть цели
`gp-gophprofile-server` и `gp-gophprofile-worker` в состоянии `UP`.

#### Дашборды

Чарт кладёт их в ConfigMap с меткой `grafana_dashboard: "1"`, а сайдкар Grafana
подгружает их сам — импортировать вручную ничего не нужно. Оба дашборда
находятся поиском по тегу `gophprofile`.

| Дашборд | Что показывает |
|---|---|
| **GophProfile — кластер** | поды по фазам и готовности; реплики Deployment и StatefulSet; текущие и желаемые реплики HPA на фоне его границ; рестарты; CPU относительно requests и память относительно лимитов по каждому поду; горутины; запросы по репликам сервера и их доля; состояние выключателей зависимостей |
| **GophProfile — обзор** | загрузки и отказы, размеры файлов, длительность и результаты обработки, отставание очереди, публикация событий и очередь разбора, HTTP-запросы, доля 5xx и задержка по маршрутам |

CPU и память сервера и воркера на дашборде кластера берутся из метрик
самого процесса (`process_cpu_seconds_total`, `process_resident_memory_bytes`),
а не из cAdvisor. Для контейнера с одним процессом на Go числа те же,
а дашборд работает и там, где kubelet не отдаёт контейнерные метрики,
как в OrbStack. CPU сравнивается с requests: лимита CPU в чарте нет намеренно,
а HPA считает загрузку именно от requests.

#### Алерты

Правила приезжают объектом `PrometheusRule` и отбирают метрики только
своего неймспейса. Сработавшие алерты уходят в Alertmanager стека.

| Алерт | Условие | Ждёт | Важность | Что проверить |
|---|---|---|---|---|
| `GophProfileTargetDown` | Prometheus не может снять метрики с пода сервера или воркера | 2 мин | critical | `kubectl -n gophprofile get pods`, события пода, пробы |
| `GophProfileHighErrorRate` | больше 5% ответов — 5xx | 5 мин | critical | логи сервера, `/readyz`, дашборд обзора |
| `GophProfilePodRestarting` | больше двух рестартов контейнера за 15 минут | 5 мин | warning | `kubectl logs --previous`, причина завершения (OOMKilled — поднять лимит памяти) |
| `GophProfileCircuitOpen` | выключатель зависимости разомкнут | 1 мин | critical | доступность Postgres, MinIO или RabbitMQ — какой именно, видно в метке `dependency` |
| `GophProfileProcessingBacklog` | больше 10 аватарок ждут обработки дольше допустимого | 5 мин | warning | жив ли воркер, есть ли соединение с брокером |
| `GophProfileEventsDeadLettered` | событие исчерпало попытки и ушло в очередь разбора | сразу | critical | очередь `avatars.dead` в RabbitMQ, логи воркера |
| `GophProfileUploadFailureRate` | больше 10% загрузок завершаются ошибкой | 5 мин | warning | хранилище и база, выключатели |
| `GophProfileProcessingTooSlow` | p95 построения миниатюр дольше 5 секунд | 10 мин | warning | CPU воркера относительно requests, размер входящих файлов |
| `GophProfileAutoscalerAtMax` | HPA сервера упёрся в максимум реплик | 15 мин | warning | запаса на следующий всплеск нет: поднять `maxReplicas` или requests |

#### Проверка балансировки

Ingress-контроллер раскладывает запросы по всем готовым репликам сервера.
Проверить можно на трёх репликах:

```bash
kubectl -n gophprofile patch hpa gp-gophprofile-server --type=merge -p '{"spec":{"minReplicas":3}}'

for i in $(seq 1 240); do
  curl -s -o /dev/null -H "Host: avatars.local" "http://$IP/api/v1/users/u$((i % 7))/avatar"
  sleep 0.25
done

# вернуть значение из values: helm upgrade ручную правку не откатывает
kubectl -n gophprofile patch hpa gp-gophprofile-server --type=merge -p '{"spec":{"minReplicas":1}}'
```

Панель «Запросы по репликам сервера» на дашборде кластера показывает
почти одинаковые линии. На локальном стенде 240 запросов разошлись
по 1,23–1,27 запроса в секунду на под. То же видно в Prometheus напрямую:

```promql
sum by (pod) (rate(http_requests_total{namespace="gophprofile", component="server"}[1m]))
```

### Окружения

| Файл | Для чего |
|---|---|
| [values.yaml](deploy/helm/gophprofile/values.yaml) | значения по умолчанию, без секретов |
| [values-dev.yaml](deploy/helm/gophprofile/values-dev.yaml) | локальный кластер: PostgreSQL, MinIO и RabbitMQ едут вместе с релизом |
| [values-prod.yaml](deploy/helm/gophprofile/values-prod.yaml) | прод: зависимости и секреты живут снаружи релиза |

```bash
make k8s-deploy K8S_VALUES=deploy/helm/gophprofile/values-prod.yaml
```

Для прода Secret заводится вне чарта — через External Secrets, Sealed Secrets
или SOPS — и указывается в `secrets.existingSecret`. Пароль, попавший в values,
попадает и в историю релизов Helm, и в логи CI.

### Команды

```bash
make k8s-lint     # helm lint + template на всех наборах values
make k8s-render   # регенерация плоских манифестов
make k8s-deploy   # неймспейс + установка
make k8s-status   # поды, HPA, ingress, сервисы
make k8s-logs     # логи сервера и воркера
make k8s-delete   # удалить релиз; неймспейс и тома остаются
```

> **Сетевые политики в OrbStack не применяются.** Объекты принимаются
> API-сервером, но трафик после них продолжает ходить: энфорсинг обеспечивает
> CNI, а k3s в этой сборке его не делает. Локально проверяется только применение
> манифестов; настоящая блокировка требует кластера с Calico или Cilium.

## Архитектура

### Поток данных

```
        HTTP                     RabbitMQ
клиент ──────► server ──┬──────► avatars.process ──────► worker
                        │                                  │
                        ├──► PostgreSQL (метаданные) ◄──────┤
                        └──► MinIO / S3 (файлы) ◄───────────┘
```

### Развёртывание в кластере

```
                    ┌─ namespace gophprofile ─────────────────────────────────┐
                    │  pod-security.kubernetes.io/enforce: restricted         │
                    │  NetworkPolicy: default-deny + точечные разрешения       │
                    │                                                         │
   Интернет         │   ┌───────────────┐   HPA 2–10 по CPU 70% / RAM 80%     │
      │             │   │  Deployment   │◄──────────── HorizontalPodAutoscaler │
      ▼             │   │    server     │                                     │
 ┌─────────┐  :8080 │   │  ┌─────────┐  │   :8081  ┌──────────────────┐       │
 │ Ingress ├────────┼──►│  │ :8080   │  ├─────────►│  ServiceMonitor  │       │
 │  nginx  │        │   │  │ :8081   │  │          └────────┬─────────┘       │
 └─────────┘        │   │  └─────────┘  │                   │                 │
   proxy-body-      │   └───┬───────────┘                   │                 │
   size: 10m        │       │ PDB minAvailable 1            │                 │
                    │       ▼                               │                 │
                    │   ┌────────────┐  ┌────────────┐      │                 │
                    │   │ StatefulSet│  │ StatefulSet│      │                 │
                    │   │ postgresql │  │  rabbitmq  │◄──┐  │                 │
                    │   └────────────┘  └────────────┘   │  │                 │
                    │   ┌────────────┐                   │  │                 │
                    │   │ StatefulSet│   ┌───────────────┴┐ │                 │
                    │   │   minio    │◄──┤   Deployment   ├─┘                 │
                    │   └────────────┘   │     worker     │  :8081            │
                    │                    │  (без :8080)   │                   │
                    │   ConfigMap ───────┴────────────────┘                   │
                    │   Secret     (db-password, s3-*, rabbitmq-url)          │
                    │   ServiceAccount без монтирования токена                │
                    │   Job migrate — Helm-хук, до выката подов               │
                    └────────────────┬────────────────────────────────────────┘
                                     │ только из namespace monitoring
                                     ▼
                         Prometheus + Grafana + Alertmanager
                         PrometheusRule: 9 правил алертов
```

Границу неймспейса держит `NetworkPolicy`: вход на 8080 разрешён только
из неймспейса ingress-контроллера, вход на 8081 — только из мониторинга,
выход — на kube-dns и на зависимости своего релиза. Публичного выхода
в интернет у подов нет.

`handlers → services → repository`, пакет `domain` не зависит ни от чего внутри проекта.

Загрузка идёт в порядке «файл в хранилище → строка в базе → событие в очередь».
Событие может не доехать, поэтому воркер раз в минуту забирает аватарки, застрявшие
в `pending` дольше пяти минут, и публикует их заново. Обработчик идемпотентен:
повторная доставка того же сообщения ничего не портит.

Метаданные лежат в одной таблице `avatars`: идентификатор, владелец, ключи
оригинала и миниатюр в хранилище, размеры, статусы загрузки и обработки,
время мягкого удаления. Схема заводится миграцией `migrations/00001_init.sql`.

## Стек

| Слой | Выбор |
|---|---|
| Язык | Go 1.25 |
| HTTP | `go-chi/chi/v5` |
| БД | PostgreSQL 16, `jackc/pgx/v5`, миграции `pressly/goose/v3` |
| Хранилище | MinIO, `minio-go/v7` |
| Очередь | RabbitMQ, `amqp091-go` |
| Картинки | `golang.org/x/image/draw`, `CatmullRom` |
| Логи | `log/slog`, JSON |
| Тесты | `testify`, `testcontainers-go`, `mockery` |
| Оркестрация | Kubernetes 1.25+, Helm 4, ingress-nginx, Prometheus Operator |

**Ограничение:** энкодера WebP в чистом Go не существует — `x/image/webp` умеет
только декодировать. WebP принимается на загрузку, миниатюры отдаются в JPEG.

## Разработка

```bash
make up            # только инфраструктура
make run-server    # сервер локально
make run-worker    # воркер локально
make test          # go test ./... -race
make cover         # покрытие с итоговым процентом
make lint          # golangci-lint
make sec           # gosec + govulncheck
make up-obs        # окружение вместе со стеком наблюдаемости
make logs-obs      # логи стека наблюдаемости
make mocks         # перегенерировать моки
make migrate-up    # накатить миграции вручную
make k8s-lint      # проверить Helm-чарт
make k8s-deploy    # поставить в кластер
```

Те же проверки идут в CI на каждый push в `main`/`dev` и на каждый pull request:
юнит-тесты на двух версиях Go, интеграционные на testcontainers с порогом
покрытия 50%, `golangci-lint`, `gosec`, `govulncheck` и сборка образа.

Тесты репозиториев поднимают PostgreSQL и MinIO через testcontainers — нужен
запущенный Docker.

Наружу PostgreSQL проброшен на **5433**: 5432 часто занят другим проектом.
Внутри compose-сети сервисы ходят на штатный 5432.

## Устойчивость к отказам

Обращения к Postgres, S3 и RabbitMQ идут через автоматические выключатели
([sony/gobreaker](https://github.com/sony/gobreaker)), по одному на зависимость
в каждом процессе.

| Состояние | Что происходит |
|---|---|
| замкнут | запросы идут в зависимость; после `BREAKER_FAILURE_THRESHOLD` отказов подряд выключатель размыкается |
| разомкнут | зависимость не вызывается: API отвечает `503` с `Retry-After`, веб-форма — страницей «временно недоступен» |
| полуоткрыт | через `BREAKER_OPEN_TIMEOUT` пропускается `BREAKER_HALF_OPEN_REQUESTS` пробных запросов; успех замыкает, отказ снова размыкает |

Отказом считается только сбой самой зависимости. «Не найдено», «чужая аватарка»
и запрос, брошенный клиентом, счётчик не трогают: иначе пользователи, запрашивающие
несуществующие аватарки, отключали бы базу для всех остальных.

Что даёт выключатель в конкретных сценариях:

- **брокер недоступен** — загрузка не ждёт подтверждения публикации, а сразу
  завершается успешно: файл и метаданные на месте, событие позже переопубликует
  реконсилятор воркера;
- **хранилище или база недоступны** — запросы получают `503` за миллисекунды,
  не занимая соединения и горутины до таймаута, и упавшая зависимость
  поднимается без лавины повторов;
- **воркер** — сообщения, отклонённые выключателем, уходят в штатный retry
  через DLX и возвращаются, когда зависимость поднимется.

Проверка готовности (`/readyz`) ходит в зависимости напрямую, минуя выключатели:
она должна видеть настоящее состояние, а не решение выключателя.

Состояние видно в метриках `circuit_breaker_state{dependency}` (0 — замкнут,
1 — полуоткрыт, 2 — разомкнут) и `circuit_breaker_rejected_total{dependency}`.
Выключатель, разомкнутый дольше минуты, поднимает алерт `GophProfileCircuitOpen`.

## Конфигурация

Все настройки — через переменные окружения, полный список с значениями
по умолчанию в [.env.example](.env.example). Обязательных нет: сервис
поднимается на дефолтах, рассчитанных на compose.

Отдельного внимания стоят:

| Переменная | По умолчанию | Смысл |
|---|---|---|
| `APP_MAX_UPLOAD_BYTES` | `10485760` | предел размера загружаемого файла |
| `APP_MAX_IMAGE_PIXELS` | `50000000` | защита от «архивной бомбы»: проверяется по заголовку до распаковки |
| `APP_RATE_LIMIT_RPM` | `300` | запросов в минуту с адреса |
| `APP_RATE_LIMIT_UPLOAD_RPM` | `10` | отдельный лимит на загрузку — она дороже чтения |
| `APP_CORS_ORIGINS` | `http://localhost:8080` | белый список origin; `*` запрещён при `APP_ENV=prod` |
| `APP_AUTO_MIGRATE` | `true` | накатывать схему при старте; в compose выключено, там работает сервис `migrate` |
| `TRACING_ENABLED` | `true` | отправлять трейсы в коллектор |
| `TRACING_ENDPOINT` | `localhost:4317` | приёмник OTLP по gRPC |
| `TRACING_SAMPLE_RATIO` | `1.0` | доля трассируемых запросов; в разработке нужен каждый |
| `WORKER_ADMIN_ADDR` | `:8081` | служебный порт воркера: `/metrics`, `/livez`, `/readyz` |
| `APP_ADMIN_ADDR` | `:8081` | то же у сервера; наружу не публикуется |
| `APP_DRAIN_DELAY` | `0s` | пауза между отказом готовности и остановкой приёма соединений; нужна в Kubernetes, локально не нужна |
| `APP_TRUSTED_PROXY_CIDRS` | пусто | подсети прокси, чьему `X-Forwarded-For` можно верить; пусто — адрес берётся из соединения |
| `BREAKER_FAILURE_THRESHOLD` | `5` | сколько отказов подряд размыкает выключатель зависимости |
| `BREAKER_OPEN_TIMEOUT` | `30s` | сколько выключатель отклоняет обращения до пробных запросов; это же значение уходит в `Retry-After` |
| `BREAKER_HALF_OPEN_REQUESTS` | `3` | сколько пробных запросов решают, вернуть ли трафик |

Конфигурация проверяется на старте: сервис падает сразу, а не на первом запросе.
В `prod` дополнительно запрещены пароли по умолчанию и wildcard в CORS.

## Тесты

```bash
make cover
```

Покрытие — **75,2%** при требовании курса >50%. Не покрыты намеренно точки входа
`cmd/*` и сетевая часть RabbitMQ (подключение и publisher), которой нужен живой брокер.

Helm-чарт проверяется отдельно: `make k8s-lint` прогоняет `helm lint`
и `helm template` на всех наборах значений, а `values.schema.json` ловит
неверные типы и диапазоны до того, как они доедут до кластера.

## Безопасность

- формат определяется по сигнатуре файла, а не по заголовку от клиента;
- `user_id` валидируется до подстановки в ключ объекта — иначе выход за пределы каталога;
- размер изображения проверяется по заголовку до распаковки;
- лимит частоты по адресу соединения. `X-Forwarded-For` читается, только если
  подсети прокси перечислены явно в `APP_TRUSTED_PROXY_CIDRS`: без этого условия
  заголовок подставит сам клиент и получит новое ведро лимита на каждый запрос.
  В кластере список задан, а путь в обход контроллера входа закрыт сетевой
  политикой — одно без другого не работает;
- метрики и пробы вынесены на служебный порт, который не публикуется наружу;
- контейнеры работают от непривилегированного пользователя, с файловой системой
  только на чтение и без смонтированного токена доступа к API Kubernetes;
- `X-Content-Type-Options: nosniff` — сервис отдаёт чужие файлы, и браузер
  не должен угадывать их тип;
- `make sec` прогоняет `gosec` и `govulncheck`.

## Лицензия

Учебный проект.
