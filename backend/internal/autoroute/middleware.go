package autoroute

import (
	"bytes"
	"io"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

type GroupProvider interface {
	IsGroupAvailable(groupName string) bool
}

type DefaultGroupProvider struct{}

func (p *DefaultGroupProvider) IsGroupAvailable(groupName string) bool {
	return true
}

func Middleware(classifier *Classifier, configProvider func() *RouteConfig, groupProvider GroupProvider) gin.HandlerFunc {
	if groupProvider == nil {
		groupProvider = &DefaultGroupProvider{}
	}

	return func(c *gin.Context) {
		config := configProvider()

		if config == nil || !config.Enabled {
			c.Next()
			return
		}

		if !isChatCompletions(c.Request.URL.Path) {
			c.Next()
			return
		}

		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.Next()
			return
		}
		c.Request.Body.Close()

		analysis, err := classifier.Analyze(bodyBytes)
		if err != nil {
			logrus.WithError(err).Warn("Auto route: failed to analyze complexity")
			analysis = &RequestAnalysis{Level: Medium}
		}

		currentGroup := c.Param("group_name")
		if currentGroup == "" {
			c.Next()
			return
		}

		mapping, found := config.GroupMapping[currentGroup]
		if !found {
			c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			c.Next()
			return
		}

		targetGroup := selectTargetGroup(mapping, analysis.Level)
		if targetGroup == "" {
			c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			c.Next()
			return
		}

		if !groupProvider.IsGroupAvailable(targetGroup) {
			fallbackGroup := getFallbackGroup(mapping, analysis.Level, groupProvider)
			if fallbackGroup != "" {
				targetGroup = fallbackGroup
			}
		}

		c.Params = setParam(c.Params, "group_name", targetGroup)

		logrus.WithFields(logrus.Fields{
			"original_group": currentGroup,
			"target_group":   targetGroup,
			"complexity":     analysis.Level,
			"tokens":         analysis.EstimatedTokens,
			"has_tools":      analysis.HasTools,
			"has_vision":     analysis.HasVision,
		}).Debug("Auto route: redirected request")

		c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		c.Next()
	}
}

func selectTargetGroup(mapping GroupMapping, level ComplexityLevel) string {
	switch level {
	case Simple:
		return mapping.SimpleGroup
	case Medium:
		return mapping.MediumGroup
	case Complex:
		return mapping.ComplexGroup
	default:
		return mapping.MediumGroup
	}
}

func getFallbackGroup(mapping GroupMapping, level ComplexityLevel, provider GroupProvider) string {
	fallbacks := []string{
		mapping.MediumGroup,
		mapping.SimpleGroup,
		mapping.ComplexGroup,
	}

	for _, fb := range fallbacks {
		if fb != "" && provider.IsGroupAvailable(fb) {
			return fb
		}
	}

	return ""
}

func isChatCompletions(path string) bool {
	return bytes.Contains([]byte(path), []byte("chat/completions"))
}

func setParam(params gin.Params, key, value string) gin.Params {
	for i, p := range params {
		if p.Key == key {
			params[i].Value = value
			return params
		}
	}
	return append(params, gin.Param{Key: key, Value: value})
}
