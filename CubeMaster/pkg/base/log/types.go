// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

// Package log provides log
package log

type Conf struct {
	Region          string `yaml:"region"`
	Cluster         string `yaml:"cluster"`
	Module          string `yaml:"module"`
	Path            string `yaml:"path"`
	FileSize        int    `yaml:"fileSize"`
	FileNum         int    `yaml:"fileNum"`
	Level           string `yaml:"level"`
	EnableLogMetric bool   `yaml:"enableLogMetric"`
}
