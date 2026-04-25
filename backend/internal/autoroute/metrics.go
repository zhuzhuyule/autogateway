package autoroute

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	autoRouteDecisions = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "auto_route_decisions_total",
			Help: "Total number of auto route decisions",
		},
		[]string{"level", "group"},
	)

	autoRouteErrors = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "auto_route_errors_total",
			Help: "Total number of auto route errors",
		},
		[]string{"reason"},
	)

	autoRouteLatency = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "auto_route_latency_seconds",
			Help:    "Auto route latency in seconds",
			Buckets: prometheus.DefBuckets,
		},
	)

	autoRouteFallbacks = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "auto_route_fallbacks_total",
			Help: "Total number of auto route fallbacks",
		},
		[]string{"original_group", "fallback_group"},
	)
)

type Metrics struct {
	mu           sync.Mutex
	decisionBuf  map[string]int
	errorBuf     map[string]int
	fallbackBuf  map[string]int
	lastFlush    time.Time
}

func NewMetrics() *Metrics {
	return &Metrics{
		decisionBuf: make(map[string]int),
		errorBuf:    make(map[string]int),
		fallbackBuf: make(map[string]int),
		lastFlush:   time.Now(),
	}
}

func (m *Metrics) RecordDecision(level, group string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := level + ":" + group
	m.decisionBuf[key]++
	m.flushIfNeeded()
}

func (m *Metrics) RecordError(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errorBuf[reason]++
	m.flushIfNeeded()
}

func (m *Metrics) RecordFallback(originalGroup, fallbackGroup string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := originalGroup + ":" + fallbackGroup
	m.fallbackBuf[key]++
	m.flushIfNeeded()
}

func (m *Metrics) RecordLatency(d time.Duration) {
	autoRouteLatency.Observe(d.Seconds())
}

func (m *Metrics) flushIfNeeded() {
	now := time.Now()
	if now.Sub(m.lastFlush) < time.Second {
		return
	}

	for key, count := range m.decisionBuf {
		parts := split2(key, ":")
		autoRouteDecisions.WithLabelValues(parts[0], parts[1]).Add(float64(count))
	}
	m.decisionBuf = make(map[string]int)

	for reason, count := range m.errorBuf {
		autoRouteErrors.WithLabelValues(reason).Add(float64(count))
	}
	m.errorBuf = make(map[string]int)

	for key, count := range m.fallbackBuf {
		parts := split2(key, ":")
		autoRouteFallbacks.WithLabelValues(parts[0], parts[1]).Add(float64(count))
	}
	m.fallbackBuf = make(map[string]int)

	m.lastFlush = now
}

func split2(s, sep string) []string {
	for i := 0; i < len(s); i++ {
		if s[i:i+1] == sep {
			return []string{s[:i], s[i+1:]}
		}
	}
	return []string{s, ""}
}

var defaultMetrics = NewMetrics()

func RecordDecision(level, group string) {
	defaultMetrics.RecordDecision(level, group)
}

func RecordError(reason string) {
	defaultMetrics.RecordError(reason)
}

func RecordFallback(originalGroup, fallbackGroup string) {
	defaultMetrics.RecordFallback(originalGroup, fallbackGroup)
}

func RecordLatency(d time.Duration) {
	defaultMetrics.RecordLatency(d)
}
