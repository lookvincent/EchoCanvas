package gateway

import (
	"strings"

	"github.com/lookvincent/EchoCanvas/services/go-gateway/internal/model"
)

// EstimatePreFlightMaxCost returns a conservative upper bound for pre-flight budget enforcement.
func EstimatePreFlightMaxCost(callType, _ string) float64 {
	switch callType {
	case model.CallTypeImageGen:
		return 0.04
	case model.CallTypeEmbedding:
		return 0.001
	case model.CallTypeVLMJudge, model.CallTypeLLMText:
		return 0.01
	default:
		return 0.01
	}
}

// CalculateCost computes USD cost from observed token or image counts.
func CalculateCost(_, modelName, callType string, inputTokens, outputTokens, imageCount int) float64 {
	if callType == model.CallTypeImageGen {
		if imageCount <= 0 {
			imageCount = 1
		}
		return float64(imageCount) * 0.03
	}

	m := strings.ToLower(modelName)
	var inPer1K, outPer1K float64
	switch {
	case strings.Contains(m, "deepseek"):
		inPer1K = 0.00014
		outPer1K = 0.00028
	case strings.Contains(m, "gpt-4o-mini"):
		inPer1K = 0.00015
		outPer1K = 0.00060
	case strings.Contains(m, "embedding"):
		inPer1K = 0.00002
		outPer1K = 0.0
	default:
		inPer1K = 0.00050
		outPer1K = 0.00150
	}

	return (float64(inputTokens)/1000.0)*inPer1K + (float64(outputTokens)/1000.0)*outPer1K
}
