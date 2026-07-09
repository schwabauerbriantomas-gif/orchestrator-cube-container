// Package main: Docker image lifecycle management.
//
// Provides build, push, pull, list, and tag operations for container images.
// These tools complete the CI/CD pipeline: with image_build + image_push you
// can compile code → produce an image → push to a registry → deploy_from_git
// or create_container using the new image. Without these, every deploy was
// stateful and non-reproducible across nodes.
//
// The image manager works directly with the Docker Engine API. When the Cube
// backend is active, image operations fall back to CubeAPI where supported or
// return a clear error.
package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ---- Types ----

// ImageBuildResult is returned by image_build.
type ImageBuildResult struct {
	ImageID    string    `json:"image_id"`
	Tag        string    `json:"tag"`
	BuildTime  string    `json:"build_time"`
	ContextDir string    `json:"context_dir"`
	Warnings   []string  `json:"warnings,omitempty"`
	BuiltAt    time.Time `json:"built_at"`
}

// ImageInfo describes a single Docker image.
type ImageInfo struct {
	ID        string   `json:"id"`
	Tags      []string `json:"tags"`
	Size      int64    `json:"size_bytes"`
	Created   string   `json:"created"`
	InUse     bool     `json:"in_use"`
}

// ImageListResult groups images for listing.
type ImageListResult struct {
	Images []ImageInfo `json:"images"`
	Total  int         `json:"total"`
}

// ImagePushResult captures a push operation outcome.
type ImagePushResult struct {
	Registry   string    `json:"registry"`
	Tag        string    `json:"tag"`
	PushedAt   time.Time `json:"pushed_at"`
	Digest     string    `json:"digest,omitempty"`
	Size       int64     `json:"size_bytes,omitempty"`
}

// ImagePullResult captures a pull operation outcome.
type ImagePullResult struct {
	Tag      string    `json:"tag"`
	PulledAt time.Time `json:"pulled_at"`
}

// ImageTagResult captures a tag operation outcome.
type ImageTagResult struct {
	SourceTag string `json:"source_tag"`
	TargetTag string `json:"target_tag"`
}

// ---- Validation ----

// validImageRef validates a Docker image reference.
// Accepts: ubuntu:22.04, myrepo/app:v1, registry.io:5000/app:tag, app@sha256:abc
// Rejects: paths with traversal, shell metacharacters, newlines.
var imageRefPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:\-@]*$`)

func validateImageRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("image reference is required")
	}
	if len(ref) > 256 {
		return fmt.Errorf("image reference too long (max 256 chars)")
	}
	if !imageRefPattern.MatchString(ref) {
		return fmt.Errorf("invalid image reference: only alphanumeric, '.', '_', '/', ':', '-', '@' allowed")
	}
	if strings.Contains(ref, "..") {
		return fmt.Errorf("invalid image reference: '..' is not allowed")
	}
	return nil
}

// validateRegistryURL ensures the registry URL is safe (no SSRF to internal hosts).
func validateRegistryURL(registry string) error {
	if registry == "" {
		return nil // empty means Docker Hub default
	}
	// Strip protocol if present
	registry = strings.TrimPrefix(strings.TrimPrefix(registry, "https://"), "http://")
	registry = strings.Split(registry, "/")[0] // take host:port
	registry = strings.Split(registry, ":")[0] // take host
	if isPrivateHost(registry) {
		return fmt.Errorf("registry host resolves to a private/internal address: %s", registry)
	}
	return nil
}

// ---- Manager ----

var imageMgr *ImageManager

type ImageManager struct {
	backend ContainerBackend
}

func newImageManager(b ContainerBackend) *ImageManager {
	return &ImageManager{backend: b}
}

// ---- Build ----

// BuildImage creates a Docker image from a Dockerfile in contextDir.
// It tarballs the context directory and sends it to the Docker build API.
func (im *ImageManager) BuildImage(ctx context.Context, contextDir, dockerfile, tag string) (*ImageBuildResult, error) {
	absDir, err := validatePathSafe(contextDir, "/")
	if err != nil {
		return nil, fmt.Errorf("invalid context dir: %w", err)
	}
	_ = absDir // already resolved below
	if tag != "" {
		if err := validateImageRef(tag); err != nil {
			return nil, err
		}
	}
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}

	// Resolve absolute path
	absDir, err = filepath.Abs(contextDir)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve context dir: %w", err)
	}

	// Check the context directory exists and is a directory
	info, err := os.Stat(absDir)
	if err != nil {
		return nil, fmt.Errorf("context dir not accessible: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("context dir is not a directory: %s", absDir)
	}

	// Verify Dockerfile exists
	dfilePath := filepath.Join(absDir, dockerfile)
	if _, err := os.Stat(dfilePath); err != nil {
		return nil, fmt.Errorf("dockerfile not found: %w", err)
	}

	// Tarball the build context
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)

	err = filepath.Walk(absDir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Skip the root dir itself
		if path == absDir {
			return nil
		}
		// Create tar header
		relPath, err := filepath.Rel(absDir, path)
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		hdr.Name = relPath
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create build context tar: %w", err)
	}
	tw.Close()

	// Only Docker backend supports build for now
	dc, ok := im.backend.(*DockerClient)
	if !ok {
		return nil, fmt.Errorf("image_build is only supported with the Docker backend (current: %s)", im.backend.BackendName())
	}

	// Build query params
	q := url.Values{}
	q.Set("dockerfile", dockerfile)
	if tag != "" {
		q.Set("t", tag)
	}

	// POST /build
	body := bytes.NewReader(tarBuf.Bytes())
	reqURL := fmt.Sprintf("http://docker/v%s/build?%s", dc.APIVersion, q.Encode())
	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-tar")

	resp, err := dc.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("build request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("build failed (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	// Parse build output (NDJSON stream)
	var imageID string
	var warnings []string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Bytes()
		var msg struct {
			Stream string            `json:"stream"`
			Aux    struct {
				ID string `json:"ID"`
			} `json:"aux"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if msg.Error != "" {
			return nil, fmt.Errorf("build error: %s", msg.Error)
		}
		if msg.Aux.ID != "" {
			imageID = msg.Aux.ID
		}
		if strings.Contains(strings.ToLower(msg.Stream), "warning") {
			warnings = append(warnings, strings.TrimSpace(msg.Stream))
		}
	}

	return &ImageBuildResult{
		ImageID:    imageID,
		Tag:        tag,
		BuildTime:  "", // computed by caller if needed
		ContextDir: absDir,
		Warnings:   warnings,
		BuiltAt:    time.Now().UTC(),
	}, nil
}

// ---- Push ----

func (im *ImageManager) PushImage(ctx context.Context, tag, registry string) (*ImagePushResult, error) {
	if err := validateImageRef(tag); err != nil {
		return nil, err
	}
	if err := validateRegistryURL(registry); err != nil {
		return nil, err
	}

	dc, ok := im.backend.(*DockerClient)
	if !ok {
		return nil, fmt.Errorf("image_push is only supported with the Docker backend (current: %s)", im.backend.BackendName())
	}

	fullRef := tag
	if registry != "" {
		fullRef = strings.TrimSuffix(registry, "/") + "/" + tag
	}

	// Tag the image with the full registry ref first (ignore error if already tagged)
	_, _, _ = dc.dockerPost(ctx, fmt.Sprintf("/images/%s/tag?repo=%s", url.PathEscape(tag), url.PathEscape(fullRef)), nil, nil)

	// POST /images/{name}/push
	pushURL := fmt.Sprintf("http://docker/v%s/images/%s/push", dc.APIVersion, url.PathEscape(fullRef))
	req, err := http.NewRequestWithContext(ctx, "POST", pushURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create push request: %w", err)
	}
	req.Header.Set("X-Registry-Auth", "") // empty auth = use Docker config

	resp, err := dc.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("push request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("push failed (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	// Parse push output for digest
	var digest string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		var msg struct {
			Aux struct {
				Digest string `json:"Digest"`
				Tag    string `json:"Tag"`
			} `json:"aux"`
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		if msg.Error != "" {
			return nil, fmt.Errorf("push error: %s", msg.Error)
		}
		if msg.Aux.Digest != "" {
			digest = msg.Aux.Digest
		}
	}

	return &ImagePushResult{
		Registry: registry,
		Tag:      fullRef,
		PushedAt: time.Now().UTC(),
		Digest:   digest,
	}, nil
}

// ---- Pull ----

func (im *ImageManager) PullImage(ctx context.Context, tag string) (*ImagePullResult, error) {
	if err := validateImageRef(tag); err != nil {
		return nil, err
	}

	dc, ok := im.backend.(*DockerClient)
	if !ok {
		return nil, fmt.Errorf("image_pull is only supported with the Docker backend (current: %s)", im.backend.BackendName())
	}

	pullURL := fmt.Sprintf("http://docker/v%s/images/create?fromImage=%s", dc.APIVersion, url.QueryEscape(tag))
	req, err := http.NewRequestWithContext(ctx, "POST", pullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create pull request: %w", err)
	}

	resp, err := dc.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pull request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode > 299 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("pull failed (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	// Drain to ensure completion
	io.Copy(io.Discard, resp.Body)

	return &ImagePullResult{
		Tag:      tag,
		PulledAt: time.Now().UTC(),
	}, nil
}

// ---- List ----

func (im *ImageManager) ListImages(ctx context.Context, filter string) (*ImageListResult, error) {
	dc, ok := im.backend.(*DockerClient)
	if !ok {
		return nil, fmt.Errorf("image_list is only supported with the Docker backend (current: %s)", im.backend.BackendName())
	}

	q := url.Values{}
	if filter != "" {
		q.Set("filter", filter)
	}
	q.Set("all", "false") // hide intermediate layers by default

	data, err := dc.dockerGet(ctx, "/images/json", q)
	if err != nil {
		return nil, err
	}

	rawList, ok := data.([]interface{})
	if !ok {
		return &ImageListResult{Images: []ImageInfo{}, Total: 0}, nil
	}

	images := make([]ImageInfo, 0, len(rawList))
	for _, raw := range rawList {
		m, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		var tags []string
		if repoTags, ok := m["RepoTags"].([]interface{}); ok {
			for _, t := range repoTags {
				tags = append(tags, fmt.Sprintf("%v", t))
			}
		}
		size, _ := m["Size"].(float64)
		created, _ := m["Created"].(float64)
		id, _ := m["Id"].(string)
		// Check if image is in use (referenced by a container)
		inUse := false
		if containers, ok := m["Containers"].(float64); ok && containers > 0 {
			inUse = true
		}
		createdStr := ""
		if created > 0 {
			createdStr = time.Unix(int64(created), 0).UTC().Format(time.RFC3339)
		}
		images = append(images, ImageInfo{
			ID:      id,
			Tags:    tags,
			Size:    int64(size),
			Created: createdStr,
			InUse:   inUse,
		})
	}

	return &ImageListResult{
		Images: images,
		Total:  len(images),
	}, nil
}

// ---- Tag ----

func (im *ImageManager) TagImage(ctx context.Context, sourceTag, targetTag string) (*ImageTagResult, error) {
	if err := validateImageRef(sourceTag); err != nil {
		return nil, err
	}
	if err := validateImageRef(targetTag); err != nil {
		return nil, err
	}

	dc, ok := im.backend.(*DockerClient)
	if !ok {
		return nil, fmt.Errorf("image_tag is only supported with the Docker backend (current: %s)", im.backend.BackendName())
	}

	// Split target into repo:tag
	repo := targetTag
	tagPart := "latest"
	if idx := strings.LastIndex(targetTag, ":"); idx > 0 && !strings.Contains(targetTag[idx:], "/") {
		repo = targetTag[:idx]
		tagPart = targetTag[idx+1:]
	}

	q := url.Values{}
	q.Set("repo", repo)
	q.Set("tag", tagPart)

	_, _, err := dc.dockerPost(ctx, fmt.Sprintf("/images/%s/tag?%s", url.PathEscape(sourceTag), q.Encode()), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("tag failed: %w", err)
	}

	return &ImageTagResult{
		SourceTag: sourceTag,
		TargetTag: targetTag,
	}, nil
}
