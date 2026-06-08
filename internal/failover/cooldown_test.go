package failover

import (
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		parsedErr  string
		want       CooldownClass
	}{
		{"openai per-minute 429", 429, "Rate limit reached for gpt-4 in organization org-x on requests per min (RPM): Limit 500", ClassTransient},
		{"groq per-day 429", 429, "Rate limit reached for model llama-3.3-70b ... requests per day (RPD) Limit 1000", ClassExhausted},
		{"gemini tokens-per-day", 429, "Resource has been exhausted: tokens per day quota", ClassExhausted},
		{"429 no detail", 429, "Too Many Requests", ClassTransient},
		{"402 payment", 402, "", ClassPayment},
		{"429 but billing", 429, "You exceeded your current quota, please check your plan and billing details", ClassPayment},
		{"insufficient balance via 400", 400, "Account balance is insufficient", ClassPayment},
		{"500 server", 500, "internal error", ClassServerError},
		{"502 server", 502, "bad gateway", ClassServerError},
		{"network (0)", 0, "context deadline exceeded", ClassServerError},
		{"403 forbidden", 403, "invalid api key", ClassNone},
		{"401 unauth", 401, "unauthorized", ClassNone},
		{"400 bad request", 400, "invalid 'messages'", ClassNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.statusCode, c.parsedErr); got != c.want {
				t.Fatalf("Classify(%d, %q) = %v, want %v", c.statusCode, c.parsedErr, got, c.want)
			}
		})
	}
}

func TestDecide(t *testing.T) {
	p := DefaultCooldownPolicy()

	t.Run("transient default no retry-after", func(t *testing.T) {
		d, counts := p.Decide(ClassTransient, 0, 0)
		if d != 90*time.Second || counts {
			t.Fatalf("got (%v,%v), want (90s,false)", d, counts)
		}
	})
	t.Run("transient honors retry-after", func(t *testing.T) {
		d, _ := p.Decide(ClassTransient, 45*time.Second, 0)
		if d != 45*time.Second {
			t.Fatalf("got %v, want 45s", d)
		}
	})
	t.Run("transient clamps retry-after to max", func(t *testing.T) {
		d, _ := p.Decide(ClassTransient, 30*time.Minute, 0)
		if d != 10*time.Minute {
			t.Fatalf("got %v, want 10m", d)
		}
	})
	t.Run("exhausted default", func(t *testing.T) {
		d, counts := p.Decide(ClassExhausted, 0, 0)
		if d != 6*time.Hour || counts {
			t.Fatalf("got (%v,%v), want (6h,false)", d, counts)
		}
	})
	t.Run("exhausted honors retry-after", func(t *testing.T) {
		d, _ := p.Decide(ClassExhausted, 2*time.Hour, 0)
		if d != 2*time.Hour {
			t.Fatalf("got %v, want 2h", d)
		}
	})
	t.Run("payment fixed 24h", func(t *testing.T) {
		d, counts := p.Decide(ClassPayment, 0, 0)
		if d != 24*time.Hour || counts {
			t.Fatalf("got (%v,%v), want (24h,false)", d, counts)
		}
	})
	t.Run("server error escalation ladder", func(t *testing.T) {
		want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute}
		for hit, w := range want {
			d, counts := p.Decide(ClassServerError, 0, hit)
			if d != w || !counts {
				t.Fatalf("hit=%d got (%v,%v), want (%v,true)", hit, d, counts, w)
			}
		}
	})
	t.Run("none yields zero", func(t *testing.T) {
		d, counts := p.Decide(ClassNone, 0, 0)
		if d != 0 || counts {
			t.Fatalf("got (%v,%v), want (0,false)", d, counts)
		}
	})
}
