// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package network

import (
	"context"
)

type MetricCollector struct{}

func NewMetricCollector(_ string) *MetricCollector {
	return &MetricCollector{}
}

func (m *MetricCollector) AddAllMetricsToCollector() {

}

func (l *local) Report(_ context.Context, _ *MetricCollector) {

}
