package observability

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Business — метрики предметной области: то, что интересует не дежурного
// по инфраструктуре, а владельца сервиса. Технические метрики HTTP и
// среды выполнения живут отдельно, в HTTP и реестре.
//
// Все методы безопасны на nil-приёмнике: в тестах слои собираются без метрик,
// и заставлять каждый из них тащить реестр ради счётчика значило бы
// переписать половину тестов ради инкремента.
type Business struct {
	uploads            *prometheus.CounterVec
	uploadBytes        prometheus.Histogram
	processingDuration prometheus.Histogram
	processed          *prometheus.CounterVec
	thumbnails         prometheus.Counter
	deleted            prometheus.Counter
	events             *prometheus.CounterVec
	deadLettered       prometheus.Counter
	pendingBacklog     prometheus.Gauge
}

// Значения статусов вынесены в константы: опечатка в метке порождает вторую
// серию, и график молча теряет часть данных.
const (
	ResultOK      = "ok"
	ResultError   = "error"
	ResultSkipped = "skipped"

	EventUpload = "upload"
	EventDelete = "delete"
)

// NewBusiness создаёт бизнес-метрики и регистрирует их в переданном реестре.
func NewBusiness(reg prometheus.Registerer) *Business {
	b := &Business{
		uploads: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "avatar_uploads_total",
			Help: "Количество загрузок аватарок по результату.",
		}, []string{"result"}),
		uploadBytes: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "avatar_upload_bytes",
			Help: "Размер загружаемых файлов.",
			// От 32 КБ до 8 МБ: аватарки мельче измерять незачем,
			// а верх ограничен APP_MAX_UPLOAD_BYTES.
			Buckets: prometheus.ExponentialBuckets(32*1024, 2, 9),
		}),
		processingDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "avatar_processing_duration_seconds",
			Help:    "Длительность построения миниатюр воркером.",
			Buckets: prometheus.DefBuckets,
		}),
		processed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "avatar_processing_total",
			Help: "Результаты обработки аватарок воркером.",
		}, []string{"result"}),
		thumbnails: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "avatar_thumbnails_created_total",
			Help: "Количество созданных миниатюр.",
		}),
		deleted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "avatar_deleted_total",
			Help: "Количество удалённых аватарок.",
		}),
		events: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "avatar_events_published_total",
			Help: "Публикации событий в брокер по типу и результату.",
		}, []string{"kind", "result"}),
		deadLettered: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "avatar_events_dead_lettered_total",
			Help: "События, исчерпавшие попытки обработки.",
		}),
		pendingBacklog: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "avatar_processing_backlog",
			Help: "Аватарки, застрявшие в ожидании обработки дольше допустимого.",
		}),
	}

	reg.MustRegister(
		b.uploads, b.uploadBytes, b.processingDuration, b.processed,
		b.thumbnails, b.deleted, b.events, b.deadLettered, b.pendingBacklog,
	)

	return b
}

// UploadFinished отмечает завершённую загрузку.
func (b *Business) UploadFinished(result string, sizeBytes int64) {
	if b == nil {
		return
	}

	b.uploads.WithLabelValues(result).Inc()

	if result == ResultOK {
		b.uploadBytes.Observe(float64(sizeBytes))
	}
}

// AvatarDeleted отмечает мягкое удаление аватарки.
func (b *Business) AvatarDeleted() {
	if b == nil {
		return
	}

	b.deleted.Inc()
}

// EventPublished отмечает попытку публикации события.
func (b *Business) EventPublished(kind, result string) {
	if b == nil {
		return
	}

	b.events.WithLabelValues(kind, result).Inc()
}

// ProcessingFinished отмечает результат обработки и её длительность.
func (b *Business) ProcessingFinished(result string, started time.Time, thumbnails int) {
	if b == nil {
		return
	}

	b.processed.WithLabelValues(result).Inc()

	if result == ResultOK {
		b.processingDuration.Observe(time.Since(started).Seconds())
		b.thumbnails.Add(float64(thumbnails))
	}
}

// EventDeadLettered отмечает событие, ушедшее в очередь разбора.
func (b *Business) EventDeadLettered() {
	if b == nil {
		return
	}

	b.deadLettered.Inc()
}

// BacklogSize сообщает, сколько аватарок застряло в ожидании обработки.
// Именно эта метрика показывает, что воркер не справляется или упал.
func (b *Business) BacklogSize(count int) {
	if b == nil {
		return
	}

	b.pendingBacklog.Set(float64(count))
}
