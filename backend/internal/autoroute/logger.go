package autoroute

import (
	"time"

	"github.com/sirupsen/logrus"
)

type RouteLogEntry struct {
	OriginalGroup string          `json:"original_group"`
	TargetGroup   string          `json:"target_group"`
	Complexity    ComplexityLevel `json:"complexity"`
	Tokens        int             `json:"tokens"`
	HasTools      bool            `json:"has_tools"`
	HasVision     bool            `json:"has_vision"`
	FallbackUsed  bool            `json:"fallback_used,omitempty"`
}

func LogRouteDecision(entry RouteLogEntry) {
	logrus.WithFields(logrus.Fields{
		"type":            "auto_route",
		"original_group":  entry.OriginalGroup,
		"target_group":    entry.TargetGroup,
		"complexity":      entry.Complexity,
		"tokens":          entry.Tokens,
		"has_tools":       entry.HasTools,
		"has_vision":      entry.HasVision,
		"fallback_used":   entry.FallbackUsed,
	}).Debug("Auto route: redirected request")
}

func LogRouteError(reason string, err error, entry *RouteLogEntry) {
	fields := logrus.Fields{
		"type":   "auto_route_error",
		"reason": reason,
	}
	if entry != nil {
		fields["original_group"] = entry.OriginalGroup
		fields["target_group"] = entry.TargetGroup
	}
	if err != nil {
		fields["error"] = err.Error()
	}
	logrus.WithFields(fields).Warn("Auto route error")
}

func LogConfigChange(action string, oldCfg, newCfg *RouteConfig) {
	fields := logrus.Fields{
		"type":   "config_change",
		"action": action,
	}
	if oldCfg != nil {
		fields["old_enabled"] = oldCfg.Enabled
	}
	if newCfg != nil {
		fields["new_enabled"] = newCfg.Enabled
		fields["new_simple_threshold"] = newCfg.SimpleThreshold
		fields["new_complex_threshold"] = newCfg.ComplexThreshold
	}
	logrus.WithFields(fields).Info("Auto route config changed")
}

const (
	LogLevelDebug = "debug"
	LogLevelInfo  = "info"
	LogLevelWarn  = "warn"
	LogLevelError = "error"
)

type LogFormatter struct {
	ServiceName string
}

func (f *LogFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	entry.Data["service"] = f.ServiceName
	entry.Data["timestamp"] = entry.Time.Format(time.RFC3339)
	entry.Data["level"] = entry.Level.String()
	return []byte(entry.Message + "\n"), nil
}
