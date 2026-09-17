# Плоские манифесты

Здесь лежит то же самое, что и в `deploy/helm/gophprofile`, только развёрнутое
в обычные YAML-манифесты. Источник истины — чарт; эти файлы **генерируются**:

```bash
make k8s-render
```

Править их руками бессмысленно — следующий `make k8s-render` перезапишет.
Они нужны, чтобы манифесты можно было читать и применять без Helm:

```bash
kubectl apply -f namespace.yaml
kubectl apply -f rendered/ -n gophprofile
```

Набор значений для генерации — `values-dev.yaml`. Для другого окружения:

```bash
helm template gp deploy/helm/gophprofile \
  -f deploy/helm/gophprofile/values-prod.yaml -n gophprofile
```

`namespace.yaml` не генерируется и лежит рядом с рендером: Helm пишет состояние
релиза в Secret внутри целевого неймспейса раньше, чем применяет манифесты,
поэтому чарт не может создать собственный неймспейс. Подробности — в шапке файла.

`migrate-job.yaml` в рендере присутствует, но при обычном `kubectl apply` он
становится обычным Job-ом: аннотации `helm.sh/hook` понимает только Helm.
Порядок «сначала миграции, потом поды» в этом режиме придётся соблюдать руками.
