// Package main: Docker Engine API backend.
//
// This file implements the SAME method signatures as CubeClient in client.go,
// but talks to the Docker Engine API over the unix socket instead of CubeAPI.
//
// The DockerClient is compiled in by default (no build tag needed).
// It is activated at runtime by newBackend() when a Docker socket is detected.
//
// DockerClient maps Docker responses into the same JSON structure that the rest
// of the MCP server (deploy.go, backup.go, server.go) expects, so that the only
// change required to switch backends is swapping the client constructor.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DockerClient is the Docker Engine API backend. It mirrors CubeClient's method
// set but communicates with the local Docker daemon over a unix socket.
type DockerClient struct {
	SocketPath string
	APIVersion string
	HTTP       *http.Client
}

func (c *DockerClient) BackendName() string { return "docker" }

func (c *DockerClient) Endpoint() string { return c.SocketPath }

// newDockerClient builds a DockerClient pointed at the local Docker daemon.
// The unix socket path and API version are configurable via environment.
func newDockerClient() *DockerClient {
	socket := envOr("DOCKER_SOCKET", "/var/run/docker.sock")
	return newDockerClientWithTransport(socket, "unix")
}

// newDockerClientWithTransport creates a DockerClient that can talk to either
// a local unix socket or a remote TCP endpoint.
func newDockerClientWithTransport(address, transport string) *DockerClient {
	var dialContext func(ctx context.Context, _, _ string) (net.Conn, error)
	if transport == "tcp" {
		d := net.Dialer{}
		dialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			// address is already host:port
			return d.DialContext(ctx, "tcp", address)
		}
	} else {
		d := net.Dialer{}
		dialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return d.DialContext(ctx, "unix", address)
		}
	}
	return &DockerClient{
		SocketPath: address,
		APIVersion: envOr("DOCKER_API_VERSION", "v1.44"),
		HTTP: &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				DialContext: dialContext,
			},
		},
	}
}

// ---- low-level helpers ----

// dockerRequest performs an HTTP request against the Docker Engine API and
// returns the raw status code and body bytes.
func (c *DockerClient) dockerRequest(ctx context.Context, method, path string, body interface{}, query url.Values) (int, []byte, error) {
	u := "http://docker/" + c.APIVersion + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return 0, nil, fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("docker request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data, nil
}

// dockerGet performs a GET and returns the decoded JSON (or raw string if the
// body is not JSON). An HTTP error status is reported via *CubeAPIError so the
// rest of the server sees the same error type it sees from CubeClient.
func (c *DockerClient) dockerGet(ctx context.Context, path string, query url.Values) (interface{}, error) {
	status, data, err := c.dockerRequest(ctx, http.MethodGet, path, nil, query)
	if err != nil {
		return nil, err
	}
	return c.decodeOrErr(status, data)
}

// dockerPost performs a POST with a JSON body.
func (c *DockerClient) dockerPost(ctx context.Context, path string, body interface{}, query url.Values) (int, []byte, error) {
	return c.dockerRequest(ctx, http.MethodPost, path, body, query)
}

// dockerDelete performs a DELETE.
func (c *DockerClient) dockerDelete(ctx context.Context, path string, query url.Values) (int, []byte, error) {
	return c.dockerRequest(ctx, http.MethodDelete, path, nil, query)
}

// decodeOrErr turns a Docker response into the same shape CubeAPI callers
// expect: a decoded JSON value, or a *CubeAPIError on 4xx/5xx.
func (c *DockerClient) decodeOrErr(status int, data []byte) (interface{}, error) {
	if status >= 400 {
		return nil, &CubeAPIError{Status: status, Detail: dockerErrorMessage(data)}
	}
	if status == 204 || len(data) == 0 {
		return map[string]interface{}{}, nil
	}
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return string(data), nil
	}
	return v, nil
}

// dockerErrorMessage extracts the human-readable "message" field from a Docker
// error body, falling back to the raw bytes.
func dockerErrorMessage(data []byte) string {
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &m) == nil && m.Message != "" {
		return m.Message
	}
	return string(data)
}

// decodeDockerStream parses Docker's multiplexed stream format (8-byte header
// per frame: 1 byte stream type, 3 bytes padding, 4-byte big-endian payload
// length). It concatenates all stdout/stderr payloads into a single string.
func decodeDockerStream(r io.Reader) string {
	var out strings.Builder
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, hdr); err != nil {
			break
		}
		size := binary.BigEndian.Uint32(hdr[4:8])
		if size == 0 {
			continue
		}
		payload := make([]byte, size)
		if _, err := io.ReadFull(r, payload); err != nil {
			break
		}
		out.Write(payload)
	}
	return out.String()
}

// ---- generic map navigation helpers ----
// toInt and toFloat are defined in backup.go and reused here.

func asMap(v interface{}) map[string]interface{} {
	if m, ok := v.(map[string]interface{}); ok {
		return m
	}
	return map[string]interface{}{}
}

func mapGet(m map[string]interface{}, key string) map[string]interface{} {
	if sub, ok := m[key].(map[string]interface{}); ok {
		return sub
	}
	return map[string]interface{}{}
}

func toString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// stripSlash removes a leading "/" from a Docker container name.
func stripSlash(name string) string {
	return strings.TrimPrefix(name, "/")
}

// firstString returns the first element of a []interface{} as a string, or "".
func firstString(arr interface{}) string {
	if s, ok := arr.([]interface{}); ok && len(s) > 0 {
		return toString(s[0])
	}
	return ""
}

// ---- Cluster endpoints ----
func (c *DockerClient) Health() (interface{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info, err := c.dockerGet(ctx, "/info", nil)
	if err != nil {
		return nil, err
	}
	m := asMap(info)
	return map[string]interface{}{
		"status":     "healthy",
		"backend":    "docker",
		"containers": mapGet(m, "Containers"),
		"images":     mapGet(m, "Images"),
		"name":       mapGet(m, "Name"),
	}, nil
}

// ClusterOverview aggregates Docker daemon info into a single-node overview.
func (c *DockerClient) ClusterOverview() (interface{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	info, err := c.dockerGet(ctx, "/info", nil)
	if err != nil {
		return nil, err
	}
	m := asMap(info)
	return map[string]interface{}{
		"nodes":             1,
		"nodesReady":        1,
		"sandboxes":         toInt(mapGet(m, "Containers")),
		"sandboxesRunning":  toInt(mapGet(m, "ContainersRunning")),
		"sandboxesPaused":   toInt(mapGet(m, "ContainersPaused")),
		"sandboxesStopped":  toInt(mapGet(m, "ContainersStopped")),
		"templates":         toInt(mapGet(m, "Images")),
		"backend":           "docker",
		"hostname":          mapGet(m, "Name"),
		"os":                mapGet(m, "OperatingSystem"),
		"arch":              mapGet(m, "Architecture"),
		"ncpu":              mapGet(m, "NCPU"),
		"memTotal":          mapGet(m, "MemTotal"),
		"serverVersion":     mapGet(m, "ServerVersion"),
	}, nil
}

// ClusterVersions returns Docker daemon version information.
func (c *DockerClient) ClusterVersions() (interface{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ver, err := c.dockerGet(ctx, "/version", nil)
	if err != nil {
		return nil, err
	}
	m := asMap(ver)
	return map[string]interface{}{
		"backend":      "docker",
		"version":      mapGet(m, "Version"),
		"apiVersion":   mapGet(m, "ApiVersion"),
		"minAPIVersion": mapGet(m, "MinAPIVersion"),
		"gitCommit":    mapGet(m, "GitCommit"),
		"goVersion":    mapGet(m, "GoVersion"),
		"os":           mapGet(m, "Os"),
		"arch":         mapGet(m, "Arch"),
		"buildTime":    mapGet(m, "BuildTime"),
	}, nil
}

// nodeFromInfo builds the single-node representation Docker exposes.
func (c *DockerClient) nodeFromInfo() (interface{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info, err := c.dockerGet(ctx, "/info", nil)
	if err != nil {
		return nil, err
	}
	m := asMap(info)
	name := toString(mapGet(m, "Name"))
	return map[string]interface{}{
		"nodeID":       name,
		"hostname":     name,
		"role":         "self",
		"state":        "ready",
		"availability": "active",
		"address":      "",
		"engineVersion": toString(mapGet(m, "ServerVersion")),
		"os":           toString(mapGet(m, "OperatingSystem")),
		"arch":         toString(mapGet(m, "Architecture")),
		"ncpu":         toInt(mapGet(m, "NCPU")),
		"memoryBytes":  toInt(mapGet(m, "MemTotal")),
		"containers":   toInt(mapGet(m, "Containers")),
	}, nil
}

// ListNodes returns a one-element slice: the local Docker daemon itself.
// Docker does not do multi-node orchestration.
func (c *DockerClient) ListNodes() (interface{}, error) {
	node, err := c.nodeFromInfo()
	if err != nil {
		return nil, err
	}
	return []interface{}{node}, nil
}

// GetNode ignores the requested ID and returns the local daemon (single node).
func (c *DockerClient) GetNode(nodeID string) (interface{}, error) {
	node, err := c.nodeFromInfo()
	if err != nil {
		return nil, err
	}
	return node, nil
}

// ---- Container lifecycle (sandboxes) ----

// dockerStateFilter maps a CubeAPI sandbox state onto a Docker status filter
// value. Docker accepts statuses like "running", "paused", "exited", etc.
func dockerStateFilter(state string) []string {
	switch strings.ToLower(state) {
	case "", "all":
		return nil
	case "running":
		return []string{"running"}
	case "paused":
		return []string{"paused"}
	case "stopped":
		return []string{"exited", "created", "dead"}
	default:
		return []string{strings.ToLower(state)}
	}
}

// ListSandboxes lists Docker containers, filtered by state, mapped to the
// sandbox JSON shape used by the rest of the MCP server.
func (c *DockerClient) ListSandboxes(state string, limit int) (interface{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	query := url.Values{}
	query.Set("all", "true")
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	if statuses := dockerStateFilter(state); len(statuses) > 0 {
		filters := map[string]interface{}{"status": statuses}
		fb, _ := json.Marshal(filters)
		query.Set("filters", string(fb))
	}

	raw, err := c.dockerGet(ctx, "/containers/json", query)
	if err != nil {
		return nil, err
	}
	arr, ok := raw.([]interface{})
	if !ok {
		return []interface{}{}, nil
	}

	out := make([]interface{}, 0, len(arr))
	for _, item := range arr {
		cm := asMap(item)
		out = append(out, map[string]interface{}{
			"sandboxID":  toString(cm["Id"]),
			"templateID": toString(cm["Image"]),
			"image":      toString(cm["Image"]),
			"name":       stripSlash(firstString(cm["Names"])),
			"state":      toString(cm["State"]),
			"status":     toString(cm["Status"]),
			"createdAt":  cm["Created"],
			"command":    toString(cm["Command"]),
			"labels":     cm["Labels"],
			"imageID":    toString(cm["ImageID"]),
		})
	}
	return out, nil
}

// GetSandbox inspects a single container and maps it to the sandbox shape,
// translating Docker resource fields (Memory bytes, NanoCpus) into the
// CubeAPI units (memoryMB, cpuCount).
func (c *DockerClient) GetSandbox(sandboxID string) (interface{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	raw, err := c.dockerGet(ctx, "/containers/"+sandboxID+"/json", nil)
	if err != nil {
		return nil, err
	}
	cm := asMap(raw)
	state := mapGet(cm, "State")
	config := mapGet(cm, "Config")
	hostConfig := mapGet(cm, "HostConfig")

	memBytes := toFloat(hostConfig["Memory"])
	nanoCpus := toFloat(hostConfig["NanoCpus"])

	return map[string]interface{}{
		"sandboxID":  toString(cm["Id"]),
		"templateID": toString(config["Image"]),
		"image":      toString(config["Image"]),
		"name":       stripSlash(toString(cm["Name"])),
		"state":      toString(state["Status"]),
		"status":     toString(state["Status"]),
		"running":    state["Running"],
		"paused":     state["Paused"],
		"createdAt":  cm["Created"],
		"startedAt":  state["StartedAt"],
		"finishedAt": state["FinishedAt"],
		"memoryMB":   memBytes / (1024.0 * 1024.0),
		"cpuCount":   nanoCpus / 1e9,
		"command":    config["Cmd"],
		"env":        config["Env"],
		"labels":     config["Labels"],
		"entrypoint": config["Entrypoint"],
		"mounts":     cm["Mounts"],
	}, nil
}

// CreateSandbox creates and starts a Docker container from a template (image),
// applying memory and CPU limits and injecting environment variables and labels.
func (c *DockerClient) CreateSandbox(templateID string, memoryMB int, cpuCount float64, envVars, metadata map[string]interface{}) (interface{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Build the environment slice as Docker expects: ["KEY=VALUE", ...].
	var env []string
	for k, v := range envVars {
		env = append(env, k+"="+toString(v))
	}

	createBody := map[string]interface{}{
		"Image": templateID,
	}
	if len(env) > 0 {
		createBody["Env"] = env
	}
	if len(metadata) > 0 {
		createBody["Labels"] = metadata
	}

	hostConfig := map[string]interface{}{}
	if memoryMB > 0 {
		// CubeAPI memoryMB → Docker Memory in bytes.
		hostConfig["Memory"] = memoryMB * 1024 * 1024
	}
	if cpuCount > 0 {
		// CubeAPI cpuCount (fractional cores) → Docker NanoCpus (billionths).
		hostConfig["NanoCpus"] = int64(cpuCount * 1e9)
	}
	if len(hostConfig) > 0 {
		createBody["HostConfig"] = hostConfig
	}

	status, data, err := c.dockerPost(ctx, "/containers/create", createBody, nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, &CubeAPIError{Status: status, Detail: dockerErrorMessage(data)}
	}

	var created struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(data, &created); err != nil || created.ID == "" {
		return nil, fmt.Errorf("could not parse container create response: %s", string(data))
	}

	// Start the container.
	startStatus, startData, err := c.dockerPost(ctx, "/containers/"+created.ID+"/start", nil, nil)
	if err != nil {
		// Remove the created-but-unstarted container to avoid orphans.
		_, _, _ = c.dockerDelete(ctx, "/containers/"+created.ID, url.Values{"force": []string{"true"}})
		return nil, err
	}
	if startStatus >= 400 {
		_, _, _ = c.dockerDelete(ctx, "/containers/"+created.ID, url.Values{"force": []string{"true"}})
		return nil, &CubeAPIError{Status: startStatus, Detail: dockerErrorMessage(startData)}
	}

	// Return the freshly-started sandbox in the canonical shape.
	return c.GetSandbox(created.ID)
}

// KillSandbox stops and removes a container (the CubeAPI Kill semantic).
func (c *DockerClient) KillSandbox(sandboxID string) (interface{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Best-effort graceful stop, then force-remove with volumes.
	stopQuery := url.Values{}
	stopQuery.Set("t", "10")
	if status, data, err := c.dockerPost(ctx, "/containers/"+sandboxID+"/stop", nil, stopQuery); err == nil && status >= 400 && status != 304 && status != 404 {
		return nil, &CubeAPIError{Status: status, Detail: dockerErrorMessage(data)}
	}

	delQuery := url.Values{}
	delQuery.Set("force", "true")
	delQuery.Set("v", "true")
	status, data, err := c.dockerDelete(ctx, "/containers/"+sandboxID, delQuery)
	if err != nil {
		return nil, err
	}
	if status >= 400 && status != 404 {
		return nil, &CubeAPIError{Status: status, Detail: dockerErrorMessage(data)}
	}
	return map[string]interface{}{
		"sandboxID": sandboxID,
		"killed":    true,
	}, nil
}

// RestartSandbox gracefully stops and starts a container without removing it.
func (c *DockerClient) RestartSandbox(sandboxID string) (interface{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Docker has a native restart endpoint with a configurable timeout.
	restartQuery := url.Values{}
	restartQuery.Set("t", "10")
	status, data, err := c.dockerPost(ctx, "/containers/"+sandboxID+"/restart", nil, restartQuery)
	if err != nil {
		return nil, err
	}
	if status >= 400 && status != 304 && status != 404 {
		return nil, &CubeAPIError{Status: status, Detail: dockerErrorMessage(data)}
	}
	return map[string]interface{}{
		"sandboxID": sandboxID,
		"restarted": true,
	}, nil
}

// PauseSandbox pauses a running container.
func (c *DockerClient) PauseSandbox(sandboxID string) (interface{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	status, data, err := c.dockerPost(ctx, "/containers/"+sandboxID+"/pause", nil, nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, &CubeAPIError{Status: status, Detail: dockerErrorMessage(data)}
	}
	return map[string]interface{}{
		"sandboxID": sandboxID,
		"paused":    true,
	}, nil
}

// ResumeSandbox unpauses a paused container.
func (c *DockerClient) ResumeSandbox(sandboxID string) (interface{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	status, data, err := c.dockerPost(ctx, "/containers/"+sandboxID+"/unpause", nil, nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, &CubeAPIError{Status: status, Detail: dockerErrorMessage(data)}
	}
	return map[string]interface{}{
		"sandboxID": sandboxID,
		"resumed":   true,
	}, nil
}

// GetSandboxLogs returns the combined stdout/stderr log lines (up to limit) of
// a container. Docker returns logs in a multiplexed stream format; we decode it
// and split into a slice of lines.
func (c *DockerClient) GetSandboxLogs(sandboxID string, limit int) (interface{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	query := url.Values{}
	query.Set("stdout", "true")
	query.Set("stderr", "true")
	query.Set("follow", "false")
	if limit > 0 {
		query.Set("tail", strconv.Itoa(limit))
	}

	status, data, err := c.dockerRequest(ctx, http.MethodGet, "/containers/"+sandboxID+"/logs", nil, query)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, &CubeAPIError{Status: status, Detail: dockerErrorMessage(data)}
	}

	text := decodeDockerStream(bytes.NewReader(data))
	var lines []string
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if lines == nil {
		lines = []string{}
	}
	return map[string]interface{}{
		"sandboxID": sandboxID,
		"logs":      lines,
	}, nil
}

// ---- Templates (images) ----

// ListTemplates lists Docker images, mapped to the template shape.
func (c *DockerClient) ListTemplates() (interface{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	raw, err := c.dockerGet(ctx, "/images/json", nil)
	if err != nil {
		return nil, err
	}
	arr, ok := raw.([]interface{})
	if !ok {
		return []interface{}{}, nil
	}

	out := make([]interface{}, 0, len(arr))
	for _, item := range arr {
		im := asMap(item)
		var aliases []string
		if tags, ok := im["RepoTags"].([]interface{}); ok {
			for _, t := range tags {
				aliases = append(aliases, toString(t))
			}
		}
		if len(aliases) == 0 {
			aliases = []string{}
		}
		out = append(out, map[string]interface{}{
			"templateID": toString(im["Id"]),
			"imageID":    toString(im["Id"]),
			"aliases":    aliases,
			"image":      firstString(aliases),
			"parent":     toString(im["ParentId"]),
			"size":       toInt(im["Size"]),
			"createdAt":  toString(im["Created"]),
			"labels":     im["Labels"],
		})
	}
	return out, nil
}

// GetTemplate inspects a single Docker image, mapped to the template shape.
func (c *DockerClient) GetTemplate(templateID string) (interface{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	raw, err := c.dockerGet(ctx, "/images/"+templateID+"/json", nil)
	if err != nil {
		return nil, err
	}
	im := asMap(raw)
	config := mapGet(im, "Config")
	var size int64
	if sz, ok := im["Size"].(float64); ok {
		size = int64(sz)
	}

	var repoTags []string
	if tags, ok := im["RepoTags"].([]interface{}); ok {
		for _, t := range tags {
			repoTags = append(repoTags, toString(t))
		}
	}
	if repoTags == nil {
		repoTags = []string{}
	}

	return map[string]interface{}{
		"templateID":  toString(im["Id"]),
		"imageID":     toString(im["Id"]),
		"aliases":     repoTags,
		"image":       firstString(repoTags),
		"parent":      toString(im["Parent"]),
		"comment":     toString(im["Comment"]),
		"created":     toString(im["Created"]),
		"os":          toString(im["Os"]),
		"arch":        toString(im["Architecture"]),
		"size":        size,
		"dockerVersion": toString(im["DockerVersion"]),
		"config":      config,
		"entrypoint":  config["Entrypoint"],
		"cmd":         config["Cmd"],
		"env":         config["Env"],
		"exposedPorts": config["ExposedPorts"],
	}, nil
}

// CreateTemplateFromImage pulls a Docker image (the closest Docker analogue to
// "creating a template"). The CubeAPI-specific options (exposePorts, mounts,
// envVars, startCmd, writableLayerSizeGB) have no direct pull-time equivalent
// in Docker and are ignored here; they would be applied at container creation
// via CreateSandbox instead.
func (c *DockerClient) CreateTemplateFromImage(image string, exposePorts []int, writableLayerSizeGB int, mounts []map[string]interface{}, envVars map[string]interface{}, startCmd string) (interface{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	imageName, tag := splitImageTag(image)
	query := url.Values{}
	query.Set("fromImage", imageName)
	if tag != "" {
		query.Set("tag", tag)
	}

	status, data, err := c.dockerRequest(ctx, http.MethodPost, "/images/create", nil, query)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, &CubeAPIError{Status: status, Detail: dockerErrorMessage(data)}
	}

	// /images/create streams newline-delimited JSON progress objects. Collect the
	// final status line for the caller.
	finalStatus := parsePullStream(data)

	ref := imageName
	if tag != "" {
		ref = imageName + ":" + tag
	}
	return map[string]interface{}{
		"templateID": ref,
		"image":      ref,
		"status":     finalStatus,
		"pulled":     true,
	}, nil
}

// splitImageTag splits "repo:tag" into (repo, tag). If no tag is present, the
// canonical Docker default "latest" is returned.
func splitImageTag(image string) (string, string) {
	// Only split on the last colon that is not part of a registry port (i.e. it
	// must not be followed by a "/"). A simple heuristic: split on the last ":"
	// when there is no "/" after it.
	idx := strings.LastIndex(image, ":")
	if idx < 0 {
		return image, "latest"
	}
	if strings.Contains(image[idx+1:], "/") {
		return image, "latest"
	}
	return image[:idx], image[idx+1:]
}

// parsePullStream extracts the final status message from a Docker image pull
// progress stream (newline-delimited JSON objects with a "status" field).
func parsePullStream(data []byte) string {
	var last string
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var obj struct {
			Status   string `json:"status"`
			ID       string `json:"id"`
			Error    string `json:"error"`
		}
		if json.Unmarshal(line, &obj) == nil {
			if obj.Error != "" {
				return obj.Error
			}
			if obj.Status != "" {
				last = obj.Status
				if obj.ID != "" {
					last = obj.ID + ": " + obj.Status
				}
			}
		}
	}
	return last
}

// ExecInSandbox runs a command inside a running container via `docker exec`,
// capturing the combined stdout/stderr and respecting the requested timeout.
func (c *DockerClient) ExecInSandbox(sandboxID, command string, timeout int) (interface{}, error) {
	timeoutDur := 30 * time.Second
	if timeout > 0 {
		timeoutDur = time.Duration(timeout) * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeoutDur)
	defer cancel()

	// 1. Create the exec instance.
	createBody := map[string]interface{}{
		"AttachStdout": true,
		"AttachStderr": true,
		"Cmd":          []string{"sh", "-c", command},
	}
	status, data, err := c.dockerPost(ctx, "/containers/"+sandboxID+"/exec", createBody, nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, &CubeAPIError{Status: status, Detail: dockerErrorMessage(data)}
	}

	var execResp struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(data, &execResp); err != nil || execResp.ID == "" {
		return nil, fmt.Errorf("could not parse exec create response: %s", string(data))
	}

	// 2. Start the exec instance and capture the multiplexed output stream.
	startStatus, startData, err := c.dockerRequest(ctx, http.MethodPost, "/exec/"+execResp.ID+"/start",
		map[string]interface{}{"Detach": false, "Tty": false}, nil)
	if err != nil {
		return nil, err
	}
	if startStatus >= 400 {
		return nil, &CubeAPIError{Status: startStatus, Detail: dockerErrorMessage(startData)}
	}

	output := decodeDockerStream(bytes.NewReader(startData))

	// 3. Best-effort inspect for the exit code (non-fatal if it fails).
	exitCode := 0
	if ins, err := c.dockerGet(ctx, "/exec/"+execResp.ID+"/json", nil); err == nil {
		exitCode = toInt(mapGet(asMap(ins), "ExitCode"))
	}

	return map[string]interface{}{
		"sandboxID": sandboxID,
		"command":   command,
		"exitCode":  exitCode,
		"stdout":    output,
	}, nil
}
