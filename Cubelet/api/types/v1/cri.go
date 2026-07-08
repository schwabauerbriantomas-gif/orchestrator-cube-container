// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package types

import (
	runtime "k8s.io/cri-api/pkg/apis/runtime/v1"
	runtimeAlpha "k8s.io/cri-api/pkg/apis/runtime/v1alpha2"
)

func (a *AuthConfig) ToCRI() *runtime.AuthConfig {
	return &runtime.AuthConfig{
		Username:      a.Username,
		Password:      a.Password,
		Auth:          a.Auth,
		ServerAddress: a.ServerAddress,
		IdentityToken: a.IdentityToken,
		RegistryToken: a.RegistryToken,
	}
}

func (a *AuthConfig) ToCRIAlpha() *runtimeAlpha.AuthConfig {
	return &runtimeAlpha.AuthConfig{
		Username:      a.Username,
		Password:      a.Password,
		Auth:          a.Auth,
		ServerAddress: a.ServerAddress,
		IdentityToken: a.IdentityToken,
		RegistryToken: a.RegistryToken,
	}
}

func (x *ImageFilter) ToCRI() *runtime.ImageFilter {
	ifer := &runtime.ImageFilter{}
	if x.Image != nil {
		ifer.Image = x.Image.ToCRI()
	}
	return ifer
}

func (x *ImageFilter) ToCRIAlpha() *runtimeAlpha.ImageFilter {
	ifer := &runtimeAlpha.ImageFilter{}
	if x.Image != nil {
		ifer.Image = x.Image.ToCRIAlpha()
	}
	return ifer
}

func (x *ImageSpec) ToCRI() *runtime.ImageSpec {
	return &runtime.ImageSpec{
		Image:       x.Image,
		Annotations: x.Annotations,
	}
}

func (x *ImageSpec) ToCRIAlpha() *runtimeAlpha.ImageSpec {
	return &runtimeAlpha.ImageSpec{
		Image:       x.Image,
		Annotations: x.Annotations,
	}
}

func FromCRIImage(cri *runtime.Image) *Image {
	img := &Image{
		Id:          cri.Id,
		RepoTags:    cri.RepoTags,
		RepoDigests: cri.RepoDigests,
		Size:        cri.Size_,
		Username:    cri.Username,
		Spec:        FromCRIImageSpec(cri.Spec),
		Pinned:      cri.Pinned,
	}
	if cri.Uid != nil {
		img.Uid = &Int64Value{
			Value: cri.Uid.Value,
		}
	}
	return img
}

func FromCRIAlphaImage(cri *runtimeAlpha.Image) *Image {
	img := &Image{
		Id:          cri.Id,
		RepoTags:    cri.RepoTags,
		RepoDigests: cri.RepoDigests,
		Size:        cri.Size_,
		Username:    cri.Username,
		Spec:        FromCRIAlphaImageSpec(cri.Spec),
		Pinned:      cri.Pinned,
	}
	if cri.Uid != nil {
		img.Uid = &Int64Value{
			Value: cri.Uid.Value,
		}
	}
	return img
}

func FromCRIImageSpec(cri *runtime.ImageSpec) *ImageSpec {
	if cri == nil {
		return nil
	}
	return &ImageSpec{
		Image:       cri.Image,
		Annotations: cri.Annotations,
	}
}

func FromCRIAlphaImageSpec(cri *runtimeAlpha.ImageSpec) *ImageSpec {
	if cri == nil {
		return nil
	}
	return &ImageSpec{
		Image:       cri.Image,
		Annotations: cri.Annotations,
	}
}

func (i *Image) ID() string {
	return i.Id
}
