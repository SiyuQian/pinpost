package platform

import "testing"

func TestRateLimiterIsPerKeyAndPerWindow(t *testing.T) {
	rl := NewRateLimiter(3)
	for i := 0; i < 3; i++ {
		if !rl.Allow("proj|1.1.1.1") {
			t.Fatalf("hit %d refused under the limit", i+1)
		}
	}
	if rl.Allow("proj|1.1.1.1") {
		t.Fatal("fourth hit allowed over a limit of 3")
	}
	if !rl.Allow("proj|2.2.2.2") {
		t.Fatal("another caller was throttled by the first one's count")
	}
	rl.window-- // as if the minute had turned
	if !rl.Allow("proj|1.1.1.1") {
		t.Fatal("the count carried over into the next window")
	}
}
