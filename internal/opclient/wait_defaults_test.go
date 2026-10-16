// The defaults of the wait live behind unexported helpers, so they are pinned
// from inside the package: the doc comment of WaitForIncomingPayment promises
// that a zero Timeout means fifteen seconds and a zero Interval means one
// second, and the fifteen second budget is too long to pin by timing it.
package opclient

import (
	"strings"
	"testing"
	"time"
)

func TestEffectiveWaitDefaults(t *testing.T) {
	t.Run("a zero timeout means fifteen seconds", func(t *testing.T) {
		got, err := effectiveWaitTimeout(0)
		if err != nil {
			t.Fatalf("effectiveWaitTimeout(0) returned an error: %v", err)
		}
		if got != 15*time.Second {
			t.Errorf("effectiveWaitTimeout(0) = %s, want 15s", got)
		}
	})

	t.Run("a zero interval means one second", func(t *testing.T) {
		got, err := effectiveWaitInterval(0)
		if err != nil {
			t.Fatalf("effectiveWaitInterval(0) returned an error: %v", err)
		}
		if got != time.Second {
			t.Errorf("effectiveWaitInterval(0) = %s, want 1s", got)
		}
	})

	t.Run("the values the caller passes are kept", func(t *testing.T) {
		timeout, err := effectiveWaitTimeout(3 * time.Second)
		if err != nil {
			t.Fatalf("effectiveWaitTimeout(3s) returned an error: %v", err)
		}
		if timeout != 3*time.Second {
			t.Errorf("effectiveWaitTimeout(3s) = %s, want 3s", timeout)
		}
		interval, err := effectiveWaitInterval(10 * time.Millisecond)
		if err != nil {
			t.Fatalf("effectiveWaitInterval(10ms) returned an error: %v", err)
		}
		if interval != 10*time.Millisecond {
			t.Errorf("effectiveWaitInterval(10ms) = %s, want 10ms", interval)
		}
	})

	t.Run("a negative budget is rejected", func(t *testing.T) {
		_, err := effectiveWaitTimeout(-time.Second)
		if err == nil {
			t.Fatal("effectiveWaitTimeout(-1s) accepted a negative timeout, want an error")
		}
		if !strings.Contains(err.Error(), "Timeout") {
			t.Errorf("error %q does not name Timeout", err)
		}
		_, err = effectiveWaitInterval(-time.Second)
		if err == nil {
			t.Fatal("effectiveWaitInterval(-1s) accepted a negative interval, want an error")
		}
		if !strings.Contains(err.Error(), "Interval") {
			t.Errorf("error %q does not name Interval", err)
		}
	})
}
