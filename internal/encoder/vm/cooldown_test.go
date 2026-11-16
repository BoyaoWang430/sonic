//go:build !sonic_compat && ((amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package vm

import (
	"testing"
)

func TestRootPlanCooldownDeterministicClock(t *testing.T) {
	var planA, planB, planC rootEncPlan

	// 1. Steady-state uncontended acquisition.
	if !planA.tryAcquireDirectSingleAt(1_000_000) {
		t.Fatal("expected uncontended planA to acquire direct path")
	}
	if planA.cooldownUntilNano.Load() != 0 {
		t.Fatalf("expected cooldownUntilNano == 0, got %d", planA.cooldownUntilNano.Load())
	}

	// 2. Contention while planA is in direct use sets a 2ms Plan-local lease.
	const tConflictA = int64(10_000_000)
	if planA.tryAcquireDirectSingleAt(tConflictA) {
		t.Fatal("expected concurrent caller on planA to back off")
	}
	wantUntilA := tConflictA + encConcCooldownWindow
	if got := planA.cooldownUntilNano.Load(); got != wantUntilA {
		t.Fatalf("cooldownUntilNano = %d, want %d", got, wantUntilA)
	}
	planA.inDirectUse.Store(0)

	// Put planB and planC into active cooldown as well at staggered timestamps.
	planB.inDirectUse.Store(1)
	_ = planB.tryAcquireDirectSingleAt(tConflictA + 500_000)
	planB.inDirectUse.Store(0)

	planC.inDirectUse.Store(1)
	_ = planC.tryAcquireDirectSingleAt(tConflictA + 900_000)
	planC.inDirectUse.Store(0)

	// 3. Before lease expiry, interleaved calls across cooling plans stay on pooled path.
	for step := int64(1); step < encConcCooldownWindow; step += 250_000 {
		now := tConflictA + step
		if planA.tryAcquireDirectSingleAt(now) {
			t.Fatalf("planA recovered prematurely at now=%d < %d", now, wantUntilA)
		}
		_ = planB.tryAcquireDirectSingleAt(now)
		_ = planC.tryAcquireDirectSingleAt(now)
	}

	// 4. Regardless of any B/C interleaving, the VERY FIRST call to planA at now >= wantUntilA recovers.
	for i := 0; i < 17; i++ {
		_ = planB.tryAcquireDirectSingleAt(wantUntilA - 1)
		_ = planC.tryAcquireDirectSingleAt(wantUntilA - 1)
	}
	if !planA.tryAcquireDirectSingleAt(wantUntilA) {
		t.Fatal("expected planA to deterministically recover on first call at lease expiry")
	}
	if planA.cooldownUntilNano.Load() != 0 {
		t.Fatalf("expected planA cooldownUntilNano cleared to 0, got %d", planA.cooldownUntilNano.Load())
	}

	// 5. Contention at expiry renews lease for another window while planA is held.
	tRenew := wantUntilA + 100_000
	if planA.tryAcquireDirectSingleAt(tRenew) {
		t.Fatal("expected contention on held planA to fail")
	}
	planA.inDirectUse.Store(0)
	if planA.tryAcquireDirectSingleAt(tRenew + encConcCooldownWindow - 1) {
		t.Fatal("expected renewed lease to hold until full window")
	}
	if !planA.tryAcquireDirectSingleAt(tRenew + encConcCooldownWindow) {
		t.Fatal("expected planA to recover immediately after renewed window")
	}
	planA.inDirectUse.Store(0)
}
