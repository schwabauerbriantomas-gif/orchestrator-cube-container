// Copyright (c) 2026 Cube Container Contributors
// SPDX-License-Identifier: Apache-2.0
//
// Fallback for building without CGO (container mode).
// nsenter is only needed for MicroVM mount namespace operations.

//go:build !cgo

package nsenter
