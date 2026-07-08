// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package components

import (
	"context"
	"os"
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/controller/nodedistribution/distribution"
	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/controller/runtemplate/templatetypes"
)

func setupTestManager(t *testing.T) (*ComponentManager, *ComponentManagerConfig, string) {
	tempDir := t.TempDir()
	config := &ComponentManagerConfig{
		VersionedBaseDir:          path.Join(tempDir, "versioned"),
		EnableFallbackRetry:       false,
		FallbackRetryComponentDir: path.Join(tempDir, "fallback"),
	}

	require.NoError(t, os.MkdirAll(config.VersionedBaseDir, 0755))
	require.NoError(t, os.MkdirAll(config.FallbackRetryComponentDir, 0755))

	manager := NewComponentManager(config)
	require.NotNil(t, manager)
	return manager, config, tempDir
}

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()
	assert.Equal(t, "/usr/local/services/cubetoolbox", config.VersionedBaseDir)
	assert.False(t, config.EnableFallbackRetry)
	assert.Equal(t, "/usr/local/services/cubetoolbox", config.FallbackRetryComponentDir)
}

func TestNewComponentManager(t *testing.T) {
	manager, config, _ := setupTestManager(t)
	assert.NotNil(t, manager)
	assert.Equal(t, config, manager.config)
}

func TestComponentManager_Handle(t *testing.T) {
	manager, config, _ := setupTestManager(t)

	require.NoError(t, os.MkdirAll(path.Join(config.VersionedBaseDir, "comp-a", "1.0"), 0755))

	require.NoError(t, os.MkdirAll(path.Join(config.FallbackRetryComponentDir, "comp-b"), 0755))

	testCases := []struct {
		name           string
		component      templatetypes.MachineComponent
		enableFallback bool
		expectErr      bool
		expectStatus   distribution.TaskStatusCode
		expectPath     string
	}{
		{
			name: "Success - Component version exists",
			component: templatetypes.MachineComponent{
				Name:    "comp-a",
				Version: "1.0",
			},
			enableFallback: false,
			expectErr:      false,
			expectStatus:   distribution.TaskStatus_SUCCESS,
			expectPath:     path.Join(config.VersionedBaseDir, "comp-a", "1.0"),
		},
		{
			name: "Failure - Component name is empty",
			component: templatetypes.MachineComponent{
				Name:    "",
				Version: "1.0",
			},
			enableFallback: false,
			expectErr:      true,
			expectStatus:   distribution.TaskStatus_FAILED,
		},
		{
			name: "Failure - Component version does not exist, no fallback",
			component: templatetypes.MachineComponent{
				Name:    "comp-a",
				Version: "2.0",
			},
			enableFallback: false,
			expectErr:      true,
			expectStatus:   distribution.TaskStatus_FAILED,
		},
		{
			name: "Success - Fallback when component version does not exist",
			component: templatetypes.MachineComponent{
				Name:    "comp-b",
				Version: "1.0",
			},
			enableFallback: true,
			expectErr:      false,
			expectStatus:   distribution.TaskStatus_SUCCESS,
			expectPath:     path.Join(config.FallbackRetryComponentDir, "comp-b"),
		},
		{
			name: "Failure - Fallback fails when component does not exist in fallback dir",
			component: templatetypes.MachineComponent{
				Name:    "comp-c",
				Version: "1.0",
			},
			enableFallback: true,
			expectErr:      true,
			expectStatus:   distribution.TaskStatus_FAILED,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			manager.config.EnableFallbackRetry = tc.enableFallback
			task := &distribution.SubTaskDefine{
				TaskCommon: distribution.TaskCommon{
					Name:       "test-task",
					TemplateID: "test-template",
				},
				Object: &tc.component,
			}

			status, err := manager.Handle(context.Background(), task)

			if tc.expectErr {
				assert.Error(t, err)
				assert.Equal(t, tc.expectStatus, status.GetStatus())
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectStatus, status.GetStatus())

				compStatus, ok := status.(*ComponentTaskStatus)
				require.True(t, ok)
				assert.Equal(t, tc.expectPath, compStatus.LocalComponent.Component.Path)
			}
		})
	}
}

func TestComponentManager_IsReady(t *testing.T) {
	tempDir := t.TempDir()
	versionedDir := path.Join(tempDir, "versioned")
	fallbackDir := path.Join(tempDir, "fallback")

	testCases := []struct {
		name            string
		setup           func()
		config          *ComponentManagerConfig
		expectedIsReady bool
		comment         string
	}{
		{
			name: "Ready when VersionedBaseDir exists",
			setup: func() {
				require.NoError(t, os.MkdirAll(versionedDir, 0755))
			},
			config: &ComponentManagerConfig{
				VersionedBaseDir: versionedDir,
			},
			expectedIsReady: true,
		},
		{
			name:  "Not Ready when VersionedBaseDir does not exist",
			setup: func() {},
			config: &ComponentManagerConfig{
				VersionedBaseDir: versionedDir,
			},
			expectedIsReady: false,
		},
		{
			name: "Not Ready when VersionedBaseDir does not exist, even if fallback is enabled and exists",
			setup: func() {
				require.NoError(t, os.MkdirAll(fallbackDir, 0755))
			},
			config: &ComponentManagerConfig{
				VersionedBaseDir:          versionedDir,
				EnableFallbackRetry:       true,
				FallbackRetryComponentDir: fallbackDir,
			},
			expectedIsReady: true,
			comment:         "This case exposes a bug in IsReady's logic, which should probably check fallback dir if primary fails.",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {

			os.RemoveAll(versionedDir)
			os.RemoveAll(fallbackDir)
			tc.setup()

			manager := NewComponentManager(tc.config)
			isReady := manager.IsReady()
			assert.Equal(t, tc.expectedIsReady, isReady, tc.comment)
		})
	}
}

func TestNewComponentTaskStatus(t *testing.T) {
	t.Run("Success case", func(t *testing.T) {
		task := &distribution.SubTaskDefine{
			TaskCommon: distribution.TaskCommon{
				Name:               "task-name",
				Namespace:          "ns",
				DistributionName:   "dist-name",
				DistributionTaskID: "dist-task-id",
				TemplateID:         "template-id",
			},
			Object: &templatetypes.MachineComponent{
				Name:    "comp-name",
				Version: "1.0",
			},
		}

		status := newComponentTaskStatus(task)

		require.NotNil(t, status)
		assert.Equal(t, distribution.TaskStatus_RUNNING, status.GetStatus())
		require.NotNil(t, status.LocalComponent)
		assert.Equal(t, "comp-name", status.LocalComponent.Component.Name)
		assert.Equal(t, "template-id", status.LocalComponent.TemplateID)
	})

	t.Run("Panic on wrong object type", func(t *testing.T) {
		task := &distribution.SubTaskDefine{
			Object: "this is not a MachineCompont",
		}
		assert.Panics(t, func() {
			newComponentTaskStatus(task)
		})
	})
}
