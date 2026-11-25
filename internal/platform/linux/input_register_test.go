//go:build linux

package linux

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Right after setup writes the .desktop file the portal may not know the
// app id yet: Register is retried, and if it never succeeds Input fails
// rather than binding shortcuts for whatever app started Footman (ADR-0030).
func TestPortalRegisterRetriesThenFails(t *testing.T) {
	oldWait, oldRetry := registerWait, registerRetry
	registerWait, registerRetry = 300*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { registerWait, registerRetry = oldWait, oldRetry })

	r := newInputRig(t, "", false)
	r.portal.set(func(f *fakePortal) { f.refuseRegister = 3 })
	if err := r.in.PrepareInsert(context.Background()); err != nil {
		t.Fatalf("PrepareInsert after 3 refusals: %v", err)
	}
	if reg := r.portal.get(func(f *fakePortal) any { return f.registered }).([]string); len(reg) != 1 || reg[0] != AppID {
		t.Errorf("registered %v", reg)
	}

	r2 := newInputRig(t, "", false)
	r2.portal.set(func(f *fakePortal) { f.refuseRegister = -1 })
	err := r2.in.PrepareInsert(context.Background())
	if err == nil || !strings.Contains(err.Error(), "does not accept the app id") {
		t.Errorf("PrepareInsert with Register always refused = %v", err)
	}
	if n := r2.portal.get(func(f *fakePortal) any { return len(f.sessions) }).(int); n != 0 {
		t.Errorf("%d portal sessions opened without the app id", n)
	}
}
