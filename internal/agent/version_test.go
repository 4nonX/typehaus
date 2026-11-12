package agent

import "testing"

func TestNewerVersion(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"0.2.0", "0.1.0", true}, {"v0.2.0", "0.2.0", false}, {"0.10.0", "0.9.9", true},
		{"0.2.0", "0.2.1", false}, {"edge", "0.1.0", false}, {"0.2.0", "dev", false}, {"1.0.0-rc1", "0.9.0", true},
	} {
		if got := newerVersion(c.a, c.b); got != c.want {
			t.Errorf("newerVersion(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
