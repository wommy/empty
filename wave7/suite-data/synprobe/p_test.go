package probe

import (
	"testing"
	"testing/synctest"
)

func TestSkipInBubble(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t.Cleanup(func() { t.Log("cleanup ran") })
		t.Skip("needs the simulator")
	})
}
