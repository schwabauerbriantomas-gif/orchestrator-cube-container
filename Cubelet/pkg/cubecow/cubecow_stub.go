// Copyright (c) 2026 Cube Container Contributors
// SPDX-License-Identifier: Apache-2.0
//
// OverlayFS stub for cubecow: makes all cubecow calls succeed as no-ops.
// In container mode, storage is handled natively by containerd's overlayfs
// snapshotter, so cubecow is never needed.

//go:build !cgo

package cubecow

func Init(string) (*Engine, error) {
	return &Engine{}, nil
}

func InitWithoutLogging(string) (*Engine, error) {
	return &Engine{}, nil
}

func InitFromJSON(string) (*Engine, error) {
	return &Engine{}, nil
}

func InitWithoutLoggingFromJSON(string) (*Engine, error) {
	return &Engine{}, nil
}

func (e *Engine) Close() {}

func (e *Engine) ResetNodeStorage() error {
	return nil
}

func (e *Engine) CreateVolume(string, uint64) (string, error) {
	return "", nil
}

func (e *Engine) DeleteVolume(string) error {
	return nil
}

func (e *Engine) ResizeVolume(string, uint64) (uint64, uint64, error) {
	return 0, 0, nil
}

func (e *Engine) GetVolumeInfo(string) (*Volume, error) {
	return &Volume{}, nil
}

func (e *Engine) GetVolumeBlockInfo(string) (*VolumeBlockInfo, error) {
	return &VolumeBlockInfo{}, nil
}

func (e *Engine) ListVolumes(uint64, string) (*ListVolumesResult, error) {
	return &ListVolumesResult{}, nil
}

func (e *Engine) CreateSnapshot(string, string, bool) (string, error) {
	return "", nil
}

func (e *Engine) ActivateVolume(string) (string, error) {
	return "/dev/null", nil
}

func (e *Engine) DeactivateVolume(string) error {
	return nil
}

func (e *Engine) DeleteSnapshot(string) error {
	return nil
}

func (e *Engine) ListSnapshots(string, uint64, string) (*ListSnapshotsResult, error) {
	return &ListSnapshotsResult{}, nil
}

func (e *Engine) GetMetrics() (map[string]uint64, error) {
	return map[string]uint64{}, nil
}
