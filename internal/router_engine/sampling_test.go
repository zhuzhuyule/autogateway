package router_engine

import (
	"math"
	"testing"
)

func avgBeta(a, b float64, n int) float64 {
	sum := 0.0
	for i := 0; i < n; i++ {
		sum += sampleBeta(a, b)
	}
	return sum / float64(n)
}

func TestSampleBetaMeans(t *testing.T) {
	if m := avgBeta(100, 1, 5000); m < 0.95 {
		t.Fatalf("Beta(100,1) mean=%.3f, want >0.95", m)
	}
	if m := avgBeta(1, 100, 5000); m > 0.05 {
		t.Fatalf("Beta(1,100) mean=%.3f, want <0.05", m)
	}
	if m := avgBeta(1, 1, 5000); math.Abs(m-0.5) > 0.05 {
		t.Fatalf("Beta(1,1) mean=%.3f, want ~0.5", m)
	}
}

func TestSampleBetaRange(t *testing.T) {
	for i := 0; i < 1000; i++ {
		v := sampleBeta(2, 5)
		if v < 0 || v > 1 {
			t.Fatalf("sampleBeta out of [0,1]: %v", v)
		}
	}
}

func TestPriorScores(t *testing.T) {
	if perfScore("high") != 1.0 || perfScore("entry") != 0.7 || perfScore("") != 0.85 {
		t.Fatal("perfScore mapping wrong")
	}
	if latencyScore("< 1s") != 1.0 || latencyScore("3-10s") != 0.7 || latencyScore("") != 0.9 {
		t.Fatal("latencyScore mapping wrong")
	}
}

func TestPriorWeightNilMeta(t *testing.T) {
	if w := priorWeight(nil); w != 1.0 {
		t.Fatalf("priorWeight(nil)=%v, want 1.0", w)
	}
	if w := priorWeight(&ModelMeta{PerformanceLevel: "high", EstimatedLatency: "< 1s"}); w != 1.0 {
		t.Fatalf("priorWeight(high,<1s)=%v, want 1.0", w)
	}
}

func TestSpeedFactor(t *testing.T) {
	if speedFactor(0) != 1.0 {
		t.Fatal("no-sample speedFactor should be 1.0")
	}
	fast, slow := speedFactor(500), speedFactor(8000)
	if !(fast > slow) {
		t.Fatalf("faster should score higher: fast=%v slow=%v", fast, slow)
	}
	if slow < 0.5 {
		t.Fatalf("speedFactor should clamp >=0.5, got %v", slow)
	}
}
