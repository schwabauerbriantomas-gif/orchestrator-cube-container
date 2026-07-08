// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package runtime

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/containerd/log"
	"github.com/containerd/plugin"
	"github.com/containerd/plugin/registry"
	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
	"k8s.io/klog/v2"

	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/containerd/containerd/v2/plugins"
	"github.com/containerd/containerd/v2/plugins/services/warning"
	"github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	cubeconfig "github.com/tencentcloud/CubeSandbox/Cubelet/internal/cube/config"

	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/constants"
)

func init() {
	config := cubeconfig.DefaultRuntimeConfig()

	registry.Register(&plugin.Registration{
		Type:   constants.CubeServicePlugin,
		ID:     "runtime",
		Config: &config,
		Requires: []plugin.Type{
			plugins.WarningPlugin,
		},
		InitFn: initCRIRuntime,
	})
}

func initCRIRuntime(ic *plugin.InitContext) (interface{}, error) {
	ic.Meta.Platforms = []imagespec.Platform{platforms.DefaultSpec()}
	ic.Meta.Exports = map[string]string{"CubeVersion": constants.CubeVersion}
	ctx := ic.Context
	pluginConfig := ic.Config.(*cubeconfig.RuntimeConfig)
	if warnings, err := cubeconfig.ValidateRuntimeConfig(ctx, pluginConfig); err != nil {
		return nil, fmt.Errorf("invalid plugin config: %w", err)
	} else if len(warnings) > 0 {
		ws, err := ic.GetSingle(plugins.WarningPlugin)
		if err != nil {
			return nil, err
		}
		warn := ws.(warning.Service)
		for _, w := range warnings {
			warn.Emit(ctx, w)
		}
	}

	containerdRootDir := filepath.Dir(ic.Properties[plugins.PropertyRootDir])
	rootDir := filepath.Join(containerdRootDir, "io.containerd.grpc.v1.cube")
	containerdStateDir := filepath.Dir(ic.Properties[plugins.PropertyStateDir])
	stateDir := filepath.Join(containerdStateDir, "io.containerd.grpc.v1.cube")
	c := cubeconfig.Config{
		RuntimeConfig:      *pluginConfig,
		ContainerdRootDir:  containerdRootDir,
		ContainerdEndpoint: ic.Properties[plugins.PropertyGRPCAddress],
		RootDir:            rootDir,
		StateDir:           stateDir,
	}

	cfg, _ := json.Marshal(c)
	log.G(ctx).WithFields(log.Fields{"config": string(cfg)}).Info("starting cri plugin")

	if err := setGLogLevel(); err != nil {
		return nil, fmt.Errorf("failed to set glog level: %w", err)
	}

	ociSpec, err := loadBaseOCISpecs(&c)
	if err != nil {
		return nil, fmt.Errorf("failed to create load basic oci spec: %w", err)
	}

	return &runtime{
		config:       c,
		baseOCISpecs: ociSpec,
	}, nil
}

type runtime struct {
	config cubeconfig.Config

	baseOCISpecs map[string]*oci.Spec
}

func (r *runtime) Config() cubeconfig.Config {
	return r.config
}

func (r *runtime) LoadOCISpec(filename string) (*oci.Spec, error) {
	spec, ok := r.baseOCISpecs[filename]
	if !ok {

		return nil, errdefs.ErrNotFound
	}
	return spec, nil
}

func loadBaseOCISpecs(config *cubeconfig.Config) (map[string]*oci.Spec, error) {
	specs := map[string]*oci.Spec{}
	for _, cfg := range config.Runtimes {
		if cfg.BaseRuntimeSpec == "" {
			continue
		}

		if _, ok := specs[cfg.BaseRuntimeSpec]; ok {
			continue
		}

		spec, err := loadOCISpec(cfg.BaseRuntimeSpec)
		if err != nil {
			return nil, fmt.Errorf("failed to load base OCI spec from file: %s: %w", cfg.BaseRuntimeSpec, err)
		}

		specs[cfg.BaseRuntimeSpec] = spec
	}

	return specs, nil
}

func loadOCISpec(filename string) (*oci.Spec, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to open base OCI spec: %s: %w", filename, err)
	}
	defer file.Close()

	spec := oci.Spec{}
	if err := json.NewDecoder(file).Decode(&spec); err != nil {
		return nil, fmt.Errorf("failed to parse base OCI spec file: %w", err)
	}

	return &spec, nil
}

func setGLogLevel() error {
	l := log.GetLevel()
	fs := flag.NewFlagSet("klog", flag.PanicOnError)
	klog.InitFlags(fs)
	if err := fs.Set("logtostderr", "true"); err != nil {
		return err
	}
	switch l {
	case log.TraceLevel:
		return fs.Set("v", "5")
	case log.DebugLevel:
		return fs.Set("v", "4")
	case log.InfoLevel:
		return fs.Set("v", "2")
	default:

	}
	return nil
}
