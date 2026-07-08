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
	t.Run("正常创建imageScore实例", func(t *testing.T) {

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

	t.Run("配置为空时panic", func(t *testing.T) {

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
	t.Run("配置为空时返回错误", func(t *testing.T) {
		originalConfig := config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore
		defer func() {
			config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = originalConfig
		}()

		config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = nil

		weight, err := getImageScoreTotalWeight()
		assert.Error(t, err)
		assert.Equal(t, 0.0, weight)
	})

	t.Run("正常计算总权重", func(t *testing.T) {
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

	t.Run("配置为空时返回0", func(t *testing.T) {
		originalConfig := config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore
		defer func() {
			config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = originalConfig
		}()

		config.GetConfig().Scheduler.Score.ScorePluginConf.ImageScore = nil

		score := getImageWeightedAverageScore(ctx, nil, nil)
		assert.Equal(t, 0.0, score)
	})

	t.Run("只启用image_id权重因子", func(t *testing.T) {
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
	t.Run("空参数返回0", func(t *testing.T) {
		score := getImageScore(ctx, nil, nil)
		assert.Equal(t, 0.0, score)
	})

	t.Run("正常计算镜像分数", func(t *testing.T) {
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
	t.Run("空参数返回0", func(t *testing.T) {
		score := getTemplateScore(ctx, "", nil)
		assert.Equal(t, 0.0, score)
	})

	t.Run("正常计算模板分数", func(t *testing.T) {
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
			name:          "分数低于最小值时使用最小值",
			sumScores:     10 * 1024 * 1024,
			numContainers: 1,
			expectedScore: 0,
		},
		{
			name:          "分数在范围内时正常计算",
			sumScores:     500 * 1024 * 1024,
			numContainers: 1,
			expectedScore: fwk.MaxNodeScore * (500*1024*1024 - minThreshold) / (maxContainerThreshold - minThreshold),
		},
		{
			name:          "分数超过最大值时使用最大值",
			sumScores:     2000 * 1024 * 1024,
			numContainers: 1,
			expectedScore: fwk.MaxNodeScore,
		},
		{
			name:          "多容器时调整最大阈值",
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
	t.Run("镜像状态为空时返回0", func(t *testing.T) {
		nodeInfo := &node.Node{}
		images := []*selctx.ImageSpec{
			{ImageID: "nginx:latest"},
			{ImageID: "redis:latest"},
		}

		sum := sumImageScores(nodeInfo, images)
		assert.Equal(t, int64(0), sum)
	})

	t.Run("空镜像列表返回0", func(t *testing.T) {
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
	t.Run("模板状态为空时返回0", func(t *testing.T) {
		nodeInfo := &node.Node{}
		templateID := "template-123"

		sum := sumTemplateScores(nodeInfo, templateID)
		assert.Equal(t, int64(0), sum)
	})

	t.Run("空模板ID返回0", func(t *testing.T) {
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

	t.Run("空亲和性配置返回空节点列表", func(t *testing.T) {
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

	t.Run("panic恢复测试", func(t *testing.T) {
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

	t.Run("正常计算节点分数 - 镜像亲和性", func(t *testing.T) {
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

	t.Run("正常计算节点分数 - 模板亲和性", func(t *testing.T) {
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

	t.Run("多权重因子组合计算", func(t *testing.T) {
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

	t.Run("禁用imageScore时返回空列表", func(t *testing.T) {
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
