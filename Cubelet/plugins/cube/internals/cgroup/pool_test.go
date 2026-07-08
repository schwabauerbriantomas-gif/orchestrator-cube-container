// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package cgroup

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/utils"
)

var testLock sync.Mutex

func TestPoolBasicOP(t *testing.T) {
	utils.SkipCI(t)

	ctx := context.Background()
	testLock.Lock()
	defer testLock.Unlock()

	testDb := filepath.Join(t.TempDir(), "db")

	var (
		cg  *uint32
		err error
	)

	db, err := utils.NewCubeStoreExt(testDb, "meta.db", 10, nil)
	require.NoErrorf(t, err, "create db")

	p := &cgPool{
		initialSize:  10,
		poolV1Handle: getDefaultCgroupHandle(1),
		poolV2Handle: getDefaultCgroupHandle(2),
		db:           db,
	}
	err = p.init()
	require.NoErrorf(t, err, "init pool")

	cgList, err := p.poolV1Handle.List()
	assert.NoError(t, err)
	assert.LessOrEqual(t, 10, len(cgList))

	cg, err = p.Get(ctx, "test1", false, 0)
	require.NoError(t, err)
	assert.Equal(t, uint32(0), *cg)
	assert.ElementsMatch(t, []uint32{0}, p.All())

	cg, err = p.Get(ctx, "test2", false, 0)
	require.NoError(t, err)
	assert.Equal(t, uint32(1), *cg)
	assert.ElementsMatch(t, []uint32{0, 1}, p.All())

	p.Put(context.TODO(), uint32(1))
	assert.ElementsMatch(t, []uint32{0}, p.All())

	cg, err = p.Get(ctx, "test3", false, 0)
	require.NoError(t, err)
	assert.Equal(t, uint32(2), *cg)
	assert.ElementsMatch(t, []uint32{0, 2}, p.All())

	err = db.Set(bucket, "test1", []byte("0"))
	require.NoError(t, err)
	err = db.Set(bucket, "test3", []byte("2"))
	require.NoError(t, err)

	p.init()
	assert.Equal(t, 2, len(p.All()))

	errs := p.Tidy()
	if len(errs) > 0 {
		err = fmt.Errorf("Tidy() returned errors: %v", errs)
	}
	assert.NoError(t, err)
	assert.ElementsMatch(t, []uint32{0, 2}, p.All())

	cgList, err = p.poolV1Handle.List()
	assert.NoError(t, err)
	assert.LessOrEqual(t, 10, len(cgList))

	p.dirtySet[0] = struct{}{}

	errs = p.Tidy()
	if len(errs) > 0 {
		err = fmt.Errorf("Tidy() returned errors: %v", errs)
	}
	assert.NoError(t, err)
	assert.ElementsMatch(t, []uint32{2}, p.All())

	cgList, err = p.poolV1Handle.List()
	assert.NoError(t, err)
	assert.LessOrEqual(t, 10, len(cgList))
}

func TestPoolTidy(t *testing.T) {
	utils.SkipCI(t)

	testLock.Lock()
	defer testLock.Unlock()

	testDb := filepath.Join(t.TempDir(), "db")

	db, err := utils.NewCubeStoreExt(testDb, "meta.db", 10, nil)
	require.NoErrorf(t, err, "create db")

	p := &cgPool{
		initialSize:  10,
		poolV1Handle: getDefaultCgroupHandle(1),
		poolV2Handle: getDefaultCgroupHandle(2),
		db:           db,
	}
	err = p.init()
	require.NoErrorf(t, err, "init pool")

	cgList, err := p.poolV1Handle.List()
	assert.NoError(t, err)
	assert.LessOrEqual(t, 11, len(cgList))

	err = p.poolV1Handle.Create(context.TODO(), MakeCgroupPoolV1PathByString("somecgunknown"))
	assert.NoError(t, err)

	p = &cgPool{
		initialSize:  0,
		poolV1Handle: getDefaultCgroupHandle(1),
		poolV2Handle: getDefaultCgroupHandle(2),
		db:           db,
	}
	err = p.init()
	require.NoErrorf(t, err, "init pool")
	errs := p.Tidy()
	if len(errs) > 0 {
		err = fmt.Errorf("Tidy() returned errors: %v", errs)
	}
	assert.NoError(t, err)

	cgList, err = p.poolV1Handle.List()
	assert.NoError(t, err)
	assert.Equal(t, 1, len(cgList))
}

func TestPoolExpand(t *testing.T) {
	utils.SkipCI(t)

	ctx := context.Background()

	testLock.Lock()
	defer testLock.Unlock()

	var (
		cg  *uint32
		err error
	)

	testDb := filepath.Join(t.TempDir(), "db")

	db, err := utils.NewCubeStoreExt(testDb, "meta.db", 10, nil)
	require.NoErrorf(t, err, "create db")

	p := &cgPool{
		initialSize:  1,
		poolV1Handle: getDefaultCgroupHandle(1),
		poolV2Handle: getDefaultCgroupHandle(2),
		db:           db,
	}
	err = p.init()
	require.NoErrorf(t, err, "init pool")

	cgList, err := p.poolV1Handle.List()
	originSize := len(cgList)
	assert.NoError(t, err)
	assert.LessOrEqual(t, 1, len(cgList))

	cg, err = p.Get(ctx, "test1", false, 0)
	require.NoError(t, err)
	assert.Equal(t, uint32(0), *cg)
	assert.ElementsMatch(t, []uint32{0}, p.All())

	cg, err = p.Get(ctx, "test2", false, 0)
	require.NoError(t, err)
	assert.Equal(t, uint32(1), *cg)
	assert.ElementsMatch(t, []uint32{0, 1}, p.All())

	cg, err = p.Get(ctx, "test3", false, 0)
	require.NoError(t, err)
	assert.Equal(t, uint32(2), *cg)
	assert.ElementsMatch(t, []uint32{0, 1, 2}, p.All())

	cgList, err = p.poolV1Handle.List()
	assert.NoError(t, err)

	assert.Equal(t, originSize+1, len(cgList))
}
