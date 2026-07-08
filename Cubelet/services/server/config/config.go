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

package config

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	srvconfig "github.com/containerd/containerd/v2/cmd/containerd/server/config"
	"github.com/containerd/log"
	"github.com/imdario/mergo"
	"github.com/pelletier/go-toml/v2"

	"github.com/containerd/errdefs"
)

type Config struct {
	*srvconfig.Config `toml:",inline"`

	PidFile string `toml:"pid_file"`

	HttpConfig HttpConfig `toml:"http"`

	CubeTap CubeTapConfig `toml:"cubetap"`

	OperationServer OperationServerConfig `toml:"operation_server"`

	DynamicConfigPath string `toml:"dynamic_config_path"`
}

type StreamProcessor struct {
	Accepts []string `toml:"accepts"`

	Returns string `toml:"returns"`

	Path string `toml:"path"`

	Args []string `toml:"args"`

	Env []string `toml:"env"`
}

type OperationServerConfig struct {
	Address string `toml:"address"`
	UID     int    `toml:"uid"`
	GID     int    `toml:"gid"`
	Disable bool   `toml:"disable"`
}

type CubeTapConfig struct {
	Address string `toml:"address"`
	UID     int    `toml:"uid"`
	GID     int    `toml:"gid"`
}

type HttpConfig struct {
	Address string `toml:"address"`
}

func (c *Config) Decode(ctx context.Context, id string, config interface{}) (interface{}, error) {
	return c.Config.Decode(ctx, id, config)
}

func LoadConfig(ctx context.Context, path string, out *Config) error {
	if out == nil {
		return fmt.Errorf("argument out must not be nil: %w", errdefs.ErrInvalidArgument)
	}

	var (
		loaded  = map[string]bool{}
		pending = []string{path}
	)

	for len(pending) > 0 {
		path, pending = pending[0], pending[1:]

		if _, ok := loaded[path]; ok {
			continue
		}

		config, err := loadConfigFile(ctx, path)
		if err != nil {
			return err
		}

		switch config.Version {
		case 0, 1:
			if err := config.MigrateConfigTo(ctx, out.Version); err != nil {
				return err
			}
		default:

		}

		if err := mergeConfig(out, config); err != nil {
			return err
		}

		imports, err := resolveImports(path, config.Imports)
		if err != nil {
			return err
		}

		loaded[path] = true
		pending = append(pending, imports...)
	}

	err := out.ValidateVersion()
	if err != nil {
		return fmt.Errorf("failed to load TOML from %s: %w", path, err)
	}
	return nil
}

func loadConfigFile(ctx context.Context, path string) (*Config, error) {
	config := &Config{}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if err := toml.NewDecoder(f).DisallowUnknownFields().Decode(config); err != nil {
		var serr *toml.StrictMissingError
		if errors.As(err, &serr) {
			for _, derr := range serr.Errors {
				row, col := derr.Position()
				log.G(ctx).WithFields(log.Fields{
					"file":   path,
					"row":    row,
					"column": col,
					"key":    strings.Join(derr.Key(), " "),
				}).WithError(err).Warn("Ignoring unknown key in TOML")
			}

			config = &Config{}
			if _, seekerr := f.Seek(0, io.SeekStart); seekerr != nil {
				return nil, fmt.Errorf("unable to seek file to start %w: failed to unmarshal TOML with unknown fields: %w", seekerr, err)
			}
			err = toml.NewDecoder(f).Decode(config)
		}
		if err != nil {
			var derr *toml.DecodeError
			if errors.As(err, &derr) {
				row, column := derr.Position()
				log.G(ctx).WithFields(log.Fields{
					"file":   path,
					"row":    row,
					"column": column,
				}).WithError(err).Error("Failure unmarshaling TOML")
				return nil, fmt.Errorf("failed to unmarshal TOML at row %d column %d: %w", row, column, err)
			}
			return nil, fmt.Errorf("failed to unmarshal TOML: %w", err)
		}

	}

	return config, nil
}

func resolveImports(parent string, imports []string) ([]string, error) {
	var out []string

	for _, path := range imports {
		path = filepath.Clean(path)
		if !filepath.IsAbs(path) {
			path = filepath.Join(filepath.Dir(parent), path)
		}

		if strings.Contains(path, "*") {
			matches, err := filepath.Glob(path)
			if err != nil {
				return nil, err
			}

			out = append(out, matches...)
		} else {
			out = append(out, path)
		}
	}

	return out, nil
}

func mergeConfig(to, from *Config) error {
	err := mergo.Merge(to, from, mergo.WithOverride, mergo.WithTransformers(sliceTransformer{}))
	if err != nil {
		return err
	}

	for k, v := range from.StreamProcessors {
		to.StreamProcessors[k] = v
	}

	for k, v := range from.ProxyPlugins {
		to.ProxyPlugins[k] = v
	}

	for k, v := range from.Timeouts {
		to.Timeouts[k] = v
	}

	return nil
}

type sliceTransformer struct{}

func (sliceTransformer) Transformer(t reflect.Type) func(dst, src reflect.Value) error {
	if t.Kind() != reflect.Slice {
		return nil
	}
	return func(dst, src reflect.Value) error {
		if !dst.CanSet() {
			return nil
		}
		if src.Type() != dst.Type() {
			return fmt.Errorf("cannot append two slice with different type (%s, %s)", src.Type(), dst.Type())
		}
		for i := 0; i < src.Len(); i++ {
			found := false
			for j := 0; j < dst.Len(); j++ {
				srcv := src.Index(i)
				dstv := dst.Index(j)
				if !srcv.CanInterface() || !dstv.CanInterface() {
					if srcv.Equal(dstv) {
						found = true
						break
					}
				} else if reflect.DeepEqual(srcv.Interface(), dstv.Interface()) {
					found = true
					break
				}
			}
			if !found {
				dst.Set(reflect.Append(dst, src.Index(i)))
			}
		}

		return nil
	}
}
