package gateway

import "testing"

func TestAcquireInstanceLockRejectsSecondHolder(t *testing.T) {
	home := t.TempDir()
	first, err := AcquireInstanceLock(home)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if pid := RunningPIDFromLock(home); pid <= 0 {
		t.Fatalf("expected pid in lock file, got %d", pid)
	}
	second, err := AcquireInstanceLock(home)
	if err == nil {
		_ = second.Close()
		t.Fatalf("expected second lock acquisition to fail")
	}
}
