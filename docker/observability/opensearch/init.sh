#!/bin/sh
# Провижининг OpenSearch: шаблон индекса и шаблон поиска в Dashboards.
# Настроенное кликами теряется вместе с томом и не воспроизводится.
set -eu

OS=${OPENSEARCH_URL:-http://opensearch:9200}
DASH=${DASHBOARDS_URL:-http://opensearch-dashboards:5601}

echo "ожидание OpenSearch..."
until curl -sf "$OS/_cluster/health" >/dev/null; do sleep 3; done

echo "шаблон индекса"
curl -sf -X PUT "$OS/_index_template/gophprofile-logs" \
  -H 'Content-Type: application/json' \
  --data-binary @/config/index-template.json >/dev/null
echo "  готово"

echo "ожидание Dashboards..."
until curl -sf "$DASH/api/status" >/dev/null; do sleep 3; done

# Шаблон поиска: без него в интерфейсе не по чему искать, и проверяющему
# пришлось бы заводить его руками.
echo "шаблон поиска gophprofile-logs-*"
curl -sf -X POST "$DASH/api/saved_objects/index-pattern/gophprofile-logs" \
  -H 'Content-Type: application/json' \
  -H 'osd-xsrf: true' \
  -d '{"attributes":{"title":"gophprofile-logs-*","timeFieldName":"@timestamp"}}' >/dev/null \
  && echo "  создан" || echo "  уже существует"

echo "провижининг завершён"
