// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package utils

import (
	"sync"
	"testing"
)

func TestResourceLock(t *testing.T) {
	locks := NewResourceLocks()

	const (
		resource = "xxx"
		n        = 10000
	)

	var result int64

	wg := sync.WaitGroup{}
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()

			unlock := locks.Lock(resource)
			defer unlock()

			result++
		}()
	}
	wg.Wait()

	if result != n || locks.Len() != 0 {
		t.Fatal("lock failed")
	}
}

func TestResourceLocks(t *testing.T) {
	locks := NewResourceLocks()
	const n = 10000

	var (
		results   = [3]int64{}
		resources = [3]string{"aaa", "bbb", "ccc"}
	)

	wg := sync.WaitGroup{}
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(x int) {
			defer wg.Done()

			i := x % len(resources)
			unlock := locks.Lock(resources[i])
			defer unlock()

			results[i]++
		}(i)
	}
	wg.Wait()

	if results[0]+results[1]+results[2] != n || locks.Len() != 0 {
		t.Fatal("lock failed")
	}
}
