package observability

import (
	"bufio"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/storage"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	registry *prometheus.Registry

	MembershipNodes      prometheus.Gauge
	NodeReady            prometheus.Gauge
	LeaseRenewalFailures prometheus.Counter
	OwnershipAttempts    *prometheus.CounterVec
	ActiveRooms          prometheus.Gauge
	ActiveClients        prometheus.Gauge
	RoomQueueDepth       prometheus.Gauge
	SlowClients          prometheus.Counter
	SnapshotOperations   *prometheus.CounterVec
	ReplayDuration       prometheus.Histogram
	RecoveryDuration     prometheus.Histogram
	QUICConnections      prometheus.Gauge
	QUICConnectionsTotal prometheus.Counter
	QUICEvents           *prometheus.CounterVec
	RedisOperations      *prometheus.HistogramVec
	RedisEvents          *prometheus.CounterVec
	MySQLOperations      *prometheus.HistogramVec
	MySQLFailures        *prometheus.CounterVec
	MySQLPool            *prometheus.GaugeVec
	HTTPRequestDuration  *prometheus.HistogramVec
}

func NewMetrics() *Metrics {
	metrics := &Metrics{registry: prometheus.NewRegistry()}
	metrics.MembershipNodes = prometheus.NewGauge(prometheus.GaugeOpts{Name: "collab_membership_nodes", Help: "READY and non-READY nodes visible in membership."})
	metrics.NodeReady = prometheus.NewGauge(prometheus.GaugeOpts{Name: "collab_node_ready", Help: "Whether this node is safe to receive traffic."})
	metrics.LeaseRenewalFailures = prometheus.NewCounter(prometheus.CounterOpts{Name: "collab_etcd_lease_renewal_failures_total", Help: "Failed etcd lease confirmations."})
	metrics.OwnershipAttempts = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "collab_ownership_attempts_total", Help: "Document ownership attempts by bounded result."}, []string{"result"})
	metrics.ActiveRooms = prometheus.NewGauge(prometheus.GaugeOpts{Name: "collab_rooms_active", Help: "Active Room goroutines."})
	metrics.ActiveClients = prometheus.NewGauge(prometheus.GaugeOpts{Name: "collab_room_clients_active", Help: "Clients currently joined to Rooms."})
	metrics.RoomQueueDepth = prometheus.NewGauge(prometheus.GaugeOpts{Name: "collab_room_event_queue_depth", Help: "Aggregate queued Room events."})
	metrics.SlowClients = prometheus.NewCounter(prometheus.CounterOpts{Name: "collab_room_slow_clients_total", Help: "Clients disconnected because their send queue was full."})
	metrics.SnapshotOperations = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "collab_snapshot_operations_total", Help: "Snapshot operations by bounded result."}, []string{"result"})
	metrics.ReplayDuration = durationHistogram("collab_replay_duration_seconds", "Snapshot and operation replay duration.")
	metrics.RecoveryDuration = durationHistogram("collab_recovery_duration_seconds", "Ownership recovery duration.")
	metrics.QUICConnections = prometheus.NewGauge(prometheus.GaugeOpts{Name: "collab_quic_connections_active", Help: "Active QUIC connections."})
	metrics.QUICConnectionsTotal = prometheus.NewCounter(prometheus.CounterOpts{Name: "collab_quic_connections_total", Help: "Accepted QUIC connections."})
	metrics.QUICEvents = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "collab_quic_events_total", Help: "QUIC events by bounded event and result."}, []string{"event", "result"})
	metrics.RedisOperations = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "collab_redis_operation_duration_seconds", Help: "Redis operation duration by bounded operation.", Buckets: prometheus.DefBuckets}, []string{"operation"})
	metrics.RedisEvents = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "collab_redis_events_total", Help: "Redis coordination events by bounded event and result."}, []string{"event", "result"})
	metrics.MySQLOperations = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "collab_mysql_operation_duration_seconds", Help: "MySQL operation duration by bounded operation.", Buckets: prometheus.DefBuckets}, []string{"operation"})
	metrics.MySQLFailures = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "collab_mysql_failures_total", Help: "MySQL failures by bounded type."}, []string{"type"})
	metrics.MySQLPool = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "collab_mysql_pool_connections", Help: "MySQL connection pool state."}, []string{"state"})
	metrics.HTTPRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "collab_http_request_duration_seconds", Help: "HTTP request latency by route class and status.", Buckets: prometheus.DefBuckets}, []string{"scope", "method", "status"})
	metrics.registry.MustRegister(
		metrics.MembershipNodes, metrics.NodeReady, metrics.LeaseRenewalFailures, metrics.OwnershipAttempts,
		metrics.ActiveRooms, metrics.ActiveClients, metrics.RoomQueueDepth, metrics.SlowClients,
		metrics.SnapshotOperations, metrics.ReplayDuration, metrics.RecoveryDuration,
		metrics.QUICConnections, metrics.QUICConnectionsTotal, metrics.QUICEvents, metrics.RedisOperations, metrics.RedisEvents,
		metrics.MySQLOperations, metrics.MySQLFailures, metrics.MySQLPool, metrics.HTTPRequestDuration,
	)
	return metrics
}

func durationHistogram(name, help string) prometheus.Histogram {
	return prometheus.NewHistogram(prometheus.HistogramOpts{Name: name, Help: help, Buckets: prometheus.DefBuckets})
}

func (metrics *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(metrics.registry, promhttp.HandlerOpts{})
}

func (metrics *Metrics) QUICConnectionOpened() {
	metrics.QUICConnections.Inc()
	metrics.QUICConnectionsTotal.Inc()
}

func (metrics *Metrics) QUICConnectionClosed() { metrics.QUICConnections.Dec() }

func (metrics *Metrics) QUICEvent(event, result string) {
	metrics.QUICEvents.WithLabelValues(event, result).Inc()
}

func (metrics *Metrics) OwnershipAttempt(result string) {
	metrics.OwnershipAttempts.WithLabelValues(result).Inc()
}

func (metrics *Metrics) LeaseRenewalFailed() { metrics.LeaseRenewalFailures.Inc() }

func (metrics *Metrics) SlowClientDisconnected() { metrics.SlowClients.Inc() }

func (metrics *Metrics) SnapshotCompleted(success bool) {
	result := "success"
	if !success {
		result = "failure"
	}
	metrics.SnapshotOperations.WithLabelValues(result).Inc()
}

func (metrics *Metrics) RecoveryCompleted(duration time.Duration, success bool) {
	metrics.RecoveryDuration.Observe(duration.Seconds())
}

func (metrics *Metrics) MySQLOperation(operation string, duration time.Duration, err error) {
	metrics.MySQLOperations.WithLabelValues(operation).Observe(duration.Seconds())
	if err == nil {
		return
	}
	kind := "other"
	switch {
	case errors.Is(err, storage.ErrFenced):
		kind = "fenced"
	case errors.Is(err, storage.ErrVersionConflict):
		kind = "version_conflict"
	}
	metrics.MySQLFailures.WithLabelValues(kind).Inc()
}

func (metrics *Metrics) ObserveDBStats(stats sql.DBStats) {
	metrics.MySQLPool.WithLabelValues("max_open").Set(float64(stats.MaxOpenConnections))
	metrics.MySQLPool.WithLabelValues("open").Set(float64(stats.OpenConnections))
	metrics.MySQLPool.WithLabelValues("in_use").Set(float64(stats.InUse))
	metrics.MySQLPool.WithLabelValues("idle").Set(float64(stats.Idle))
}

func (metrics *Metrics) RedisOperation(operation string, duration time.Duration, err error) {
	metrics.RedisOperations.WithLabelValues(operation).Observe(duration.Seconds())
	if err != nil {
		metrics.RedisEvents.WithLabelValues(operation, "failure").Inc()
	}
}

func (metrics *Metrics) RedisEvent(event, result string) {
	metrics.RedisEvents.WithLabelValues(event, result).Inc()
}

func (metrics *Metrics) InstrumentHTTP(scope string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		statusWriter := &statusRecorder{ResponseWriter: writer, status: http.StatusOK}
		next.ServeHTTP(statusWriter, request)
		metrics.HTTPRequestDuration.WithLabelValues(scope, request.Method, strconv.Itoa(statusWriter.status)).Observe(time.Since(started).Seconds())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (writer *statusRecorder) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

func (writer *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	connection, buffered, err := http.NewResponseController(writer.ResponseWriter).Hijack()
	if err == nil {
		writer.status = http.StatusSwitchingProtocols
	}
	return connection, buffered, err
}

func (writer *statusRecorder) WriteHeader(status int) {
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}
