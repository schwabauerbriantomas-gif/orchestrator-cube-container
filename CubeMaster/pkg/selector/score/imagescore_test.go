// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package score

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tencentcloud/CubeSandbox/CubeMaster/pkg/base/config"
	"github.com/tencentcloud/CubeSandbox/CubeMaster/pkg/base/constants"
	fwk "github.com/tencentcloud/CubeSandbox/CubeMaster/pkg/base/framework"
	"github.com/tencentcloud/CubeSandbox/CubeMaster/pkg/base/node"
	"github.com/tencentcloud/CubeSandbox/CubeMaster/pkg/localcache"
	"github.com/tencentcloud/CubeSandbox/CubeMaster/pkg/scheduler/selctx"
)

func TestNewImageScore(t *testing.T) {
	t.Run("successfully create imageScore instance", func(t *testing.T) {

		originalConfig := config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore
		defer func() {
			config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = originalConfig
		}()

		config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = &config.ImageScore{
			Weight:              1.0,
			EnableWeightFactors: []string{"image_id", "template_id"},
			Disable:             false,
		}

		score := NewImageScore()
		assert.NotNil(t, score)
		assert.Equal(t, "Score/image_score", score.ID())
		assert.Equal(t, "Score/image_score", score.String())
		assert.Equal(t, 1.0, score.Weight())
		assert.False(t, score.Disable())
	})

	t.Run("panics when config is nil", func(t *testing.T) {

		originalConfig := config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore
		defer func() {
			config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = originalConfig
		}()

		config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = nil

		assert.Panics(t, func() {
			NewImageScore()
		})
	})
}

func TestGetImageScoreTotalWeight(t *testing.T) {
	t.Run("returns error when config is nil", func(t *testing.T) {
		originalConfig := config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore
		defer func() {
			config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = originalConfig
		}()

		config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = nil

		weight, err := getImageScoreTotalWeight()
		assert.Error(t, err)
		assert.Equal(t, 0.0, weight)
	})

	t.Run("calculate total weight normally", func(t *testing.T) {
		originalConfig := config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore
		originalWeights := config.GetConfig().Scheduler.Score.ResourceWeights
		defer func() {
			config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = originalConfig
			config.GetConfig().Scheduler.Score.ResourceWeights = originalWeights
		}()

		config.GetConfig().Scheduler.Score.ResourceWeights = map[string]float64{
			constants.WeightFactorImageID:    0.6,
			constants.WeightFactorTemplateID: 0.4,
		}
		config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = &config.ImageScore{
			EnableWeightFactors: []string{"image_id", "template_id"},
		}

		weight, err := getImageScoreTotalWeight()
		assert.NoError(t, err)
		assert.Equal(t, 1.0, weight)
	})
}

func TestGetImageWeightedAverageScore(t *testing.T) {

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	localcache.Init(ctx)

	t.Run("returns 0 when config is nil", func(t *testing.T) {
		originalConfig := config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore
		defer func() {
			config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = originalConfig
		}()

		config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = nil

		score := getImageWeightedAverageScore(ctx, nil, nil)
		assert.Equal(t, 0.0, score)
	})

	t.Run("only image_id weight factor enabled", func(t *testing.T) {
		originalConfig := config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore
		originalWeights := config.GetConfig().Scheduler.Score.ResourceWeights
		defer func() {
			config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = originalConfig
			config.GetConfig().Scheduler.Score.ResourceWeights = originalWeights
		}()

		config.GetConfig().Scheduler.Score.ResourceWeights = map[string]float64{
			constants.WeightFactorImageID: 0.8,
		}
		config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = &config.ImageScore{
			EnableWeightFactors: []string{"image_id"},
		}

		res := &selctx.RequestResource{
			ErofsImages: []*selctx.ImageSpec{
				{ImageID: "nginx:latest"},
			},
		}
		nodeInfo := &node.Node{}

		score := getImageWeightedAverageScore(ctx, res, nodeInfo)
		assert.Equal(t, 0.0, score)
	})
}

func TestGetImageScore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	localcache.Init(ctx)
	t.Run("returns 0 for nil parameters", func(t *testing.T) {
		score := getImageScore(ctx, nil, nil)
		assert.Equal(t, 0.0, score)
	})

	t.Run("calculate image score normally", func(t *testing.T) {
		images := []*selctx.ImageSpec{
			{ImageID: "nginx:latest"},
			{ImageID: "redis:latest"},
		}
		nodeInfo := &node.Node{}

		score := getImageScore(ctx, images, nodeInfo)
		assert.Equal(t, 0.0, score)
	})
}

func TestGetTemplateScore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	localcache.Init(ctx)
	t.Run("returns 0 for nil parameters", func(t *testing.T) {
		score := getTemplateScore(ctx, "", nil)
		assert.Equal(t, 0.0, score)
	})

	t.Run("calculate template score normally", func(t *testing.T) {
		templateID := "template-123"
		nodeInfo := &node.Node{}

		score := getTemplateScore(ctx, templateID, nodeInfo)
		assert.Equal(t, 0.0, score)
	})
}

func TestCalculatePriority(t *testing.T) {
	testCases := []struct {
		name          string
		sumScores     int64
		numContainers int
		expectedScore int64
	}{
		{
			name:          "score below minimum uses minimum value",
			sumScores:     10 * 1024 * 1024,
			numContainers: 1,
			expectedScore: 0,
		},
		{
			name:          "score within range calculates normally",
			sumScores:     500 * 1024 * 1024,
			numContainers: 1,
			expectedScore: fwk.MaxNodeScore * (500*1024*1024 - minThreshold) / (maxContainerThreshold - minThreshold),
		},
		{
			name:          "score exceeds maximum uses maximum value",
			sumScores:     2000 * 1024 * 1024,
			numContainers: 1,
			expectedScore: fwk.MaxNodeScore,
		},
		{
			name:          "multiple containers adjusts max threshold",
			sumScores:     500 * 1024 * 1024,
			numContainers: 2,
			expectedScore: fwk.MaxNodeScore * (500*1024*1024 - minThreshold) / (2*maxContainerThreshold - minThreshold),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			score := calculatePriority(tc.sumScores, tc.numContainers)
			assert.Equal(t, tc.expectedScore, score)
		})
	}
}

func TestSumImageScores(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	localcache.Init(ctx)
	t.Run("returns 0 when image state is empty", func(t *testing.T) {
		nodeInfo := &node.Node{}
		images := []*selctx.ImageSpec{
			{ImageID: "nginx:latest"},
			{ImageID: "redis:latest"},
		}

		sum := sumImageScores(nodeInfo, images)
		assert.Equal(t, int64(0), sum)
	})

	t.Run("returns 0 for empty image list", func(t *testing.T) {
		nodeInfo := &node.Node{}
		var images []*selctx.ImageSpec

		sum := sumImageScores(nodeInfo, images)
		assert.Equal(t, int64(0), sum)
	})
}

func TestSumTemplateScores(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	localcache.Init(ctx)
	t.Run("returns 0 when template state is empty", func(t *testing.T) {
		nodeInfo := &node.Node{}
		templateID := "template-123"

		sum := sumTemplateScores(nodeInfo, templateID)
		assert.Equal(t, int64(0), sum)
	})

	t.Run("returns 0 for empty template ID", func(t *testing.T) {
		nodeInfo := &node.Node{}
		templateID := ""

		sum := sumTemplateScores(nodeInfo, templateID)
		assert.Equal(t, int64(0), sum)
	})
}

func TestImageScoreSelect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	localcache.Init(ctx)

	t.Run("empty affinity config returns empty node list", func(t *testing.T) {
		originalConfig := config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore
		defer func() {
			config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = originalConfig
		}()

		config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = &config.ImageScore{
			Weight:              1.0,
			EnableWeightFactors: []string{"image_id"},
			Disable:             false,
		}

		score := NewImageScore()
		selCtx := &selctx.SelectorCtx{
			Ctx:    ctx,
			ReqRes: &selctx.RequestResource{},
		}

		nodes, err := score.Select(selCtx)
		assert.NoError(t, err)
		assert.Empty(t, nodes)
	})

	t.Run("panic recovery test", func(t *testing.T) {
		originalConfig := config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore
		defer func() {
			config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = originalConfig
		}()

		config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = &config.ImageScore{
			Weight:              1.0,
			EnableWeightFactors: []string{"image_id"},
			Disable:             false,
		}

		score := NewImageScore()

		selCtx := &selctx.SelectorCtx{
			Ctx:    ctx,
			ReqRes: &selctx.RequestResource{},
		}

		nodes, err := score.Select(selCtx)
		assert.NoError(t, err)
		assert.Empty(t, nodes)
	})

	t.Run("calculate node score normally - image affinity", func(t *testing.T) {
		originalConfig := config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore
		originalWeights := config.GetConfig().Scheduler.Score.ResourceWeights
		defer func() {
			config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = originalConfig
			config.GetConfig().Scheduler.Score.ResourceWeights = originalWeights
		}()

		config.GetConfig().Scheduler.Score.ResourceWeights = map[string]float64{
			constants.WeightFactorImageID: 1.0,
		}
		config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = &config.ImageScore{
			Weight:              1.0,
			EnableWeightFactors: []string{"image_id"},
			Disable:             false,
		}

		score := NewImageScore()
		selCtx := &selctx.SelectorCtx{
			Ctx: ctx,
			ReqRes: &selctx.RequestResource{
				ErofsImages: []*selctx.ImageSpec{
					{ImageID: "nginx:latest"},
					{ImageID: "redis:latest"},
				},
			},
		}

		nodeList := node.NodeList{}
		node1 := &node.Node{InsID: "node-1"}
		nodeList = append(nodeList, node1)

		node2 := &node.Node{InsID: "node-2"}
		nodeList = append(nodeList, node2)

		selCtx.SetNodes(nodeList)

		nodes, err := score.Select(selCtx)
		assert.NoError(t, err)
		assert.NotNil(t, nodes)
		assert.Equal(t, 2, nodes.Len())

		for i := 0; i < nodes.Len(); i++ {
			nodeScore := nodes[i]
			assert.NotNil(t, nodeScore)
			assert.Contains(t, []string{"node-1", "node-2"}, nodeScore.InsID)

			assert.Equal(t, 0.0, nodeScore.Score)
		}
	})

	t.Run("calculate node score normally - template affinity", func(t *testing.T) {
		originalConfig := config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore
		originalWeights := config.GetConfig().Scheduler.Score.ResourceWeights
		defer func() {
			config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = originalConfig
			config.GetConfig().Scheduler.Score.ResourceWeights = originalWeights
		}()

		config.GetConfig().Scheduler.Score.ResourceWeights = map[string]float64{
			constants.WeightFactorTemplateID: 1.0,
		}
		config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = &config.ImageScore{
			Weight:              1.0,
			EnableWeightFactors: []string{"template_id"},
			Disable:             false,
		}

		score := NewImageScore()
		selCtx := &selctx.SelectorCtx{
			Ctx: ctx,
			ReqRes: &selctx.RequestResource{
				TemplateID: "template-123",
			},
		}

		nodeList := node.NodeList{}
		node1 := &node.Node{InsID: "node-1"}
		nodeList = append(nodeList, node1)

		selCtx.SetNodes(nodeList)

		nodes, err := score.Select(selCtx)
		assert.NoError(t, err)
		assert.NotNil(t, nodes)
		assert.Equal(t, 1, nodes.Len())

		nodeScore := nodes[0]
		assert.NotNil(t, nodeScore)
		assert.Equal(t, "node-1", nodeScore.InsID)

		assert.Equal(t, 0.0, nodeScore.Score)
	})

	t.Run("combined multi-weight factor calculation", func(t *testing.T) {
		originalConfig := config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore
		originalWeights := config.GetConfig().Scheduler.Score.ResourceWeights
		defer func() {
			config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = originalConfig
			config.GetConfig().Scheduler.Score.ResourceWeights = originalWeights
		}()

		config.GetConfig().Scheduler.Score.ResourceWeights = map[string]float64{
			constants.WeightFactorImageID:    0.6,
			constants.WeightFactorTemplateID: 0.4,
		}
		config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = &config.ImageScore{
			Weight:              1.0,
			EnableWeightFactors: []string{"image_id", "template_id"},
			Disable:             false,
		}

		score := NewImageScore()
		selCtx := &selctx.SelectorCtx{
			Ctx: ctx,
			ReqRes: &selctx.RequestResource{
				ErofsImages: []*selctx.ImageSpec{
					{ImageID: "nginx:latest"},
				},
				TemplateID: "template-123",
			},
		}

		nodeList := node.NodeList{}
		node1 := &node.Node{InsID: "node-1"}
		nodeList = append(nodeList, node1)

		selCtx.SetNodes(nodeList)

		nodes, err := score.Select(selCtx)
		assert.NoError(t, err)
		assert.NotNil(t, nodes)
		assert.Equal(t, 1, nodes.Len())

		nodeScore := nodes[0]
		assert.NotNil(t, nodeScore)
		assert.Equal(t, "node-1", nodeScore.InsID)

		assert.Equal(t, 0.0, nodeScore.Score)
	})

	t.Run("returns empty list when imageScore is disabled", func(t *testing.T) {
		originalConfig := config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore
		defer func() {
			config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = originalConfig
		}()

		config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = &config.ImageScore{
			Weight:              1.0,
			EnableWeightFactors: []string{"image_id"},
			Disable:             true,
		}

		score := NewImageScore()
		selCtx := &selctx.SelectorCtx{
			Ctx: ctx,
			ReqRes: &selctx.RequestResource{
				ErofsImages: []*selctx.ImageSpec{
					{ImageID: "nginx:latest"},
				},
			},
		}

		nodeList := node.NodeList{}
		node1 := &node.Node{InsID: "node-1"}
		nodeList = append(nodeList, node1)

		selCtx.SetNodes(nodeList)

		nodes, err := score.Select(selCtx)
		assert.NoError(t, err)
		assert.Empty(t, nodes)
	})
}
