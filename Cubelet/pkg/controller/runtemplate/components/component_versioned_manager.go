// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package components

import (
	"context"
	"fmt"
	"path"

	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/controller/nodedistribution/distribution"
	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/controller/runtemplate/templatetypes"
	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/log"
	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/utils"
	"github.com/tencentcloud/CubeSandbox/cubelog"
)

type ComponentManagerConfig struct {
	VersionedBaseDir    string `toml:"versioned_base_dir"`
	EnableFallbackRetry bool   `toml:"enable_fallback_retry"`

	FallbackRetryComponentDir string `toml:"fallback_retry_component_dir"`
}

func DefaultConfig() *ComponentManagerConfig {
	return &ComponentManagerConfig{
		VersionedBaseDir:          "/usr/local/services/cubetoolbox",
		EnableFallbackRetry:       false,
		FallbackRetryComponentDir: "/usr/local/services/cubetoolbox",
	}
}

type ComponentManager struct {
	config *ComponentManagerConfig
}

func NewComponentManager(config *ComponentManagerConfig) *ComponentManager {
	cm := &ComponentManager{
		config: config,
	}
	distribution.RegisterHandler(distribution.ResourceTaskTypeComponent, cm)
	return cm
}

func (c *ComponentManager) Handle(ctx context.Context, task *distribution.SubTaskDefine) (status distribution.TaskStatus, err error) {
	baseStatus := newComponentTaskStatus(task)
	status = baseStatus
	component := baseStatus.Component

	logEntry := log.G(ctx).WithFields(CubeLog.Fields{
		"mod":       "component_manager",
		"task_id":   task.Name,
		"component": component.Name,
		"version":   component.Version,
		"template":  task.TemplateID,
	})
	defer func() {
		if err != nil {
			logEntry.Errorf("handle component task failed: %v", err)
			baseStatus.AddError(ctx, err)
		} else {
			baseStatus.SetStatus(distribution.TaskStatus_SUCCESS, "")
			logEntry.Infof("handle component task success")
		}
	}()
	if component.Name == "" {
		err = fmt.Errorf("component name is empty")
		return
	}
	componentVersionedDir := path.Join(c.config.VersionedBaseDir, component.Name, component.Version)
	var ok bool
	ok, err = utils.DenExist(componentVersionedDir)
	if !ok {
		if c.config.EnableFallbackRetry {
			componentVersionedDir = path.Join(c.config.FallbackRetryComponentDir, component.Name)
			ok, err = utils.DenExist(componentVersionedDir)
			if ok {
				baseStatus.LocalComponent.Component.Path = componentVersionedDir
				return
			}
		}
		if err == nil {
			err = fmt.Errorf("component versioned dir %s not exist", componentVersionedDir)
		}
		return
	}

	baseStatus.LocalComponent.Component.Path = componentVersionedDir
	return
}

func (c *ComponentManager) IsReady() bool {
	ok, _ := utils.DenExist(c.config.VersionedBaseDir)
	if !ok && c.config.EnableFallbackRetry {
		ok, _ = utils.DenExist(c.config.FallbackRetryComponentDir)
	}
	return ok
}

var _ distribution.TaskHandler = &ComponentManager{}

type ComponentTaskStatus struct {
	*distribution.BaseSubTaskStatus
	*templatetypes.LocalComponent
}

func newComponentTaskStatus(task *distribution.SubTaskDefine) *ComponentTaskStatus {
	return &ComponentTaskStatus{
		BaseSubTaskStatus: task.NewRunningStatus(),
		LocalComponent: &templatetypes.LocalComponent{
			DistributionReference: *task.GenDistributionReference(),
			Component:             *task.Object.(*templatetypes.MachineComponent),
		},
	}
}
