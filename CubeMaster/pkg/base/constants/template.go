// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package constants

func GetAppSnapshotVersion(annotations map[string]string) string {
	if annotations == nil {
		return ""
	}
	if v := annotations[CubeAnnotationAppSnapshotVersion]; v != "" {
		return v
	}
	return annotations[CubeAnnotationAppSnapshotTemplateVersion]
}

func HasAppSnapshotTemplateVersion(annotations map[string]string) bool {
	return GetAppSnapshotVersion(annotations) != ""
}

func SetAppSnapshotVersion(annotations map[string]string, version string) {
	if annotations == nil || version == "" {
		return
	}
	annotations[CubeAnnotationAppSnapshotVersion] = version
	annotations[CubeAnnotationAppSnapshotTemplateVersion] = version
}

func NormalizeAppSnapshotAnnotations(annotations map[string]string) {
	SetAppSnapshotVersion(annotations, GetAppSnapshotVersion(annotations))
}
