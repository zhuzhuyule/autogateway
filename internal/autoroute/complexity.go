package autoroute

import (
	"encoding/json"
	"unicode/utf8"
)

type Classifier struct {
	config *ClassifierConfig
}

func NewClassifier(cfg *ClassifierConfig) *Classifier {
	if cfg == nil {
		cfg = DefaultClassifierConfig()
	}
	return &Classifier{config: cfg}
}

func (c *Classifier) Analyze(bodyBytes []byte) (*RequestAnalysis, error) {
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content interface{}     `json:"content"`
		} `json:"messages"`
		Tools    []json.RawMessage `json:"tools"`
	}

	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		return nil, err
	}

	analysis := &RequestAnalysis{
		MessageCount: len(req.Messages),
		HasTools:     len(req.Tools) > 0,
		ToolCount:    len(req.Tools),
	}

	totalTokens := 0
	maxMsgLength := 0

	for _, msg := range req.Messages {
		var textLen int

		switch content := msg.Content.(type) {
		case string:
			textLen = utf8.RuneCountInString(content)
			totalTokens += estimateTokens(textLen)

		case []interface{}:
			for _, item := range content {
				if itemMap, ok := item.(map[string]interface{}); ok {
					if t, ok := itemMap["type"].(string); ok {
						switch t {
						case "image_url", "image":
							analysis.HasVision = true
							totalTokens += c.config.VisionComplexityWeight
						case "text":
							if text, ok := itemMap["text"].(string); ok {
								textLen = utf8.RuneCountInString(text)
								totalTokens += estimateTokens(textLen)
							}
						}
					}
				}
			}
		}
		if textLen > maxMsgLength {
			maxMsgLength = textLen
		}
	}

	totalTokens += analysis.ToolCount * c.config.ToolComplexityWeight

	analysis.EstimatedTokens = totalTokens
	analysis.MaxMsgLength = maxMsgLength
	analysis.Level = c.classifyLevel(analysis)

	return analysis, nil
}

func (c *Classifier) classifyLevel(a *RequestAnalysis) ComplexityLevel {
	if a.EstimatedTokens > c.config.ComplexTokenThreshold || a.HasVision || a.ToolCount > 3 {
		return Complex
	}

	if a.EstimatedTokens > c.config.SimpleTokenThreshold || a.ToolCount > 0 || a.MessageCount > 10 {
		return Medium
	}

	return Simple
}

func estimateTokens(runeCount int) int {
	return runeCount * 4 / 5
}
