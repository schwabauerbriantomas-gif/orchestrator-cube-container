// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package framework

import (
	"sync"
	"testing"
)

func TestImageStateSummary_ConcurrentNodesAccess(t *testing.T) {

	iss := NewImageStateSummary(0, "")

	const (
		numGoroutines = 10
		numOperations = 100
		nodePrefix    = "node-"
	)

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()

			for j := 0; j < numOperations; j++ {
				nodeName := nodePrefix + string(rune(id)) + "-" + string(rune(j))

				iss.AddNode(nodeName)

				if !iss.HasNode(nodeName) {
					t.Errorf("node %s should exist but was not found", nodeName)
				}

				iss.RemoveNode(nodeName)

				if iss.HasNode(nodeName) {
					t.Errorf("node %s should have been removed but still exists", nodeName)
				}
			}
		}(i)
	}

	wg.Wait()

	if iss.GetNumNodes() != 0 {
		t.Errorf("final Nodes set should be empty, but has %d elements", iss.GetNumNodes())
	}
}

func TestImageStateSummary_ConcurrentReadWrite(t *testing.T) {
	iss := NewImageStateSummary(0, "")

	const (
		numReaders = 5
		numWriters = 5
		numNodes   = 50
	)

	var wg sync.WaitGroup

	for i := 0; i < numNodes; i++ {
		nodeName := "preload-node-" + string(rune(i))
		iss.AddNode(nodeName)
	}

	wg.Add(numWriters)
	for i := 0; i < numWriters; i++ {
		go func(writerID int) {
			defer wg.Done()

			for j := 0; j < 20; j++ {
				nodeName := "writer-" + string(rune(writerID)) + "-node-" + string(rune(j))

				iss.AddNode(nodeName)

				if j < numNodes {
					deleteNode := "preload-node-" + string(rune(j))
					iss.RemoveNode(deleteNode)
				}
			}
		}(i)
	}

	wg.Add(numReaders)
	for i := 0; i < numReaders; i++ {
		go func(readerID int) {
			defer wg.Done()

			for j := 0; j < 30; j++ {

				length := iss.GetNumNodes()
				if length < 0 {
					t.Errorf("Nodes length must not be negative: %d", length)
				}

				if j < numNodes {
					nodeName := "preload-node-" + string(rune(j))
					_ = iss.HasNode(nodeName)
				}
			}
		}(i)
	}

	wg.Wait()

	t.Logf("concurrent read-write test completed, final Nodes count: %d", iss.GetNumNodes())
}

func TestImageStateSummary_SnapshotConcurrency(t *testing.T) {
	iss := NewImageStateSummary(1024, "")

	for i := 0; i < 10; i++ {
		nodeName := "initial-node-" + string(rune(i))
		iss.AddNode(nodeName)
	}

	const numSnapshots = 100
	var wg sync.WaitGroup
	wg.Add(numSnapshots)

	snapshots := make([]*ImageStateSummary, numSnapshots)
	for i := 0; i < numSnapshots; i++ {
		go func(index int) {
			defer wg.Done()
			snapshots[index] = iss.Snapshot()
		}(i)
	}

	wg.Wait()

	expectedNumNodes := iss.GetNumNodes()
	for i, snapshot := range snapshots {
		if snapshot.NumNodes != expectedNumNodes {
			t.Errorf("snapshot %d NumNodes mismatch: expected %d, got %d", i, expectedNumNodes, snapshot.NumNodes)
		}
		if snapshot.Size != iss.Size {
			t.Errorf("snapshot %d Size mismatch: expected %d, got %d", i, iss.Size, snapshot.Size)
		}
	}
}
