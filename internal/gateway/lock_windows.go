//go:build windows

package gateway

import "fmt"

func AcquireInstanceLock(home string) (*InstanceLock, error) {
	return nil, fmt.Errorf("gateway instance lock is not implemented on windows")
}

func (l *InstanceLock) Close() error { return nil }
