package router_engine

import (
	"math"
	"math/rand"
)

// ModelMeta 是采样需要的先验子集（来自 FreeModels registry）。
type ModelMeta struct {
	PerformanceLevel string // "entry" | "mid" | "high" | ""
	EstimatedLatency string // "< 1s" | "1-3s" | "3-10s" | ""
}

// MetaProvider 提供模型静态先验元数据。由 services.FreeModelsRegistry 适配实现。
type MetaProvider interface {
	LookupMeta(modelID string) *ModelMeta
}

func perfScore(level string) float64 {
	switch level {
	case "high":
		return 1.0
	case "mid":
		return 0.85
	case "entry":
		return 0.7
	default:
		return 0.85
	}
}

func latencyScore(l string) float64 {
	switch l {
	case "< 1s":
		return 1.0
	case "1-3s":
		return 0.85
	case "3-10s":
		return 0.7
	default:
		return 0.9
	}
}

// priorWeight = perfScore × latencyScore；meta 为 nil → 1.0（中性）。
func priorWeight(m *ModelMeta) float64 {
	if m == nil {
		return 1.0
	}
	return perfScore(m.PerformanceLevel) * latencyScore(m.EstimatedLatency)
}

// speedFactor 把实时延迟 EWMA(ms) 映射成 [0.5,1.0] 乘子；0(无样本) → 1.0。
func speedFactor(ewmaMs float64) float64 {
	if ewmaMs <= 0 {
		return 1.0
	}
	f := 1.0 - 0.5*(1.0-math.Exp(-ewmaMs/4000.0))
	if f < 0.5 {
		return 0.5
	}
	if f > 1.0 {
		return 1.0
	}
	return f
}

// sampleGamma 从 Gamma(k,1) 采样（Marsaglia-Tsang）。
func sampleGamma(k float64) float64 {
	if k < 1 {
		u := rand.Float64()
		return sampleGamma(k+1) * math.Pow(u, 1.0/k)
	}
	d := k - 1.0/3.0
	c := 1.0 / math.Sqrt(9.0*d)
	for {
		x := rand.NormFloat64()
		v := 1.0 + c*x
		if v <= 0 {
			continue
		}
		v = v * v * v
		u := rand.Float64()
		if u < 1.0-0.0331*x*x*x*x {
			return d * v
		}
		if math.Log(u) < 0.5*x*x+d*(1.0-v+math.Log(v)) {
			return d * v
		}
	}
}

// sampleBeta 从 Beta(alpha,beta) 采样：X/(X+Y), X~Gamma(a), Y~Gamma(b)。
func sampleBeta(alpha, beta float64) float64 {
	x := sampleGamma(alpha)
	y := sampleGamma(beta)
	if x+y == 0 {
		return 0.5
	}
	return x / (x + y)
}
