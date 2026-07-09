// Package main: zero-downtime rolling deployment.
//
// deploy_rollout replaces replicas of a service one at a time, waiting for
// the new replica to pass its health check before replacing the next one.
// If a new replica fails to become healthy within the timeout window, the
// rollout is aborted and already-replaced replicas stay (they are healthy).
//
// This is the tool that makes production-safe deploys possible:
//   scale_set changes the count, but deploy_rollout changes the IMAGE
//   without dropping below the desired replica count at any point.
package main

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ---- Types ----

type RolloutResult struct {
	ServiceName  string         `json:"service_name"`
	NewImage     string         `json:"new_image"`
	Strategy     string         `json:"strategy"`
	TotalReplicas int           `json:"total_replicas"`
	Succeeded    int            `json:"succeeded"`
	Failed       int            `json:"failed"`
	Aborted      bool           `json:"aborted"`
	AbortReason  string         `json:"abort_reason,omitempty"`
	History      []RolloutStep  `json:"history"`
	StartedAt    time.Time      `json:"started_at"`
	CompletedAt  time.Time      `json:"completed_at"`
	Duration     string         `json:"duration"`
}

type RolloutStep struct {
	Replica     int           `json:"replica"`
	OldContainer string       `json:"old_container"`
	NewContainer string       `json:"new_container"`
	Status       string       `json:"status"` // healthy, failed, pending
	HealthCheck  string       `json:"health_check_duration"`
}

// ---- Manager ----

var rolloutMgr *RolloutManager

type RolloutManager struct {
	mu      sync.Mutex
	backend ContainerBackend
}

func newRolloutManager(b ContainerBackend) *RolloutManager {
	return &RolloutManager{backend: b}
}

// Rollout performs a rolling update of a service.
//
// strategy: "rolling" (default, one-by-one) or "blue-green" (create all new,
//           health-check all, then switch traffic and kill old).
// healthWaitSeconds: time to wait for each new replica to become healthy.
// abortOnFailure: if true, stop immediately when a replica fails.
func (rm *RolloutManager) Rollout(
	ctx context.Context,
	serviceName, newImage string,
	strategy string,
	healthWaitSeconds int,
	abortOnFailure bool,
) (*RolloutResult, error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if serviceName == "" {
		return nil, fmt.Errorf("service_name is required")
	}
	if newImage == "" {
		return nil, fmt.Errorf("new_image is required")
	}
	if err := validateImageRef(newImage); err != nil {
		return nil, fmt.Errorf("invalid new_image: %w", err)
	}
	if strategy == "" {
		strategy = "rolling"
	}
	if strategy != "rolling" && strategy != "blue-green" {
		return nil, fmt.Errorf("strategy must be 'rolling' or 'blue-green', got: %s", strategy)
	}
	if healthWaitSeconds <= 0 {
		healthWaitSeconds = 60
	}
	if healthWaitSeconds > 600 {
		return nil, fmt.Errorf("health_wait_seconds must be <= 600 (10 min)")
	}

	// Get current service definition
	svc, err := scaleMgr.getService(serviceName)
	if err != nil {
		return nil, fmt.Errorf("cannot find service '%s': %w", serviceName, err)
	}

	result := &RolloutResult{
		ServiceName:   serviceName,
		NewImage:      newImage,
		Strategy:      strategy,
		TotalReplicas: svc.Replicas,
		StartedAt:     time.Now().UTC(),
		History:       []RolloutStep{},
	}

	oldContainers := svc.ContainerIDs
	if len(oldContainers) == 0 {
		result.Aborted = true
		result.AbortReason = "no running replicas to roll"
		result.CompletedAt = time.Now().UTC()
		return result, nil
	}

	healthTimeout := time.Duration(healthWaitSeconds) * time.Second

	switch strategy {
	case "rolling":
		for i, oldContainerID := range oldContainers {
			select {
			case <-ctx.Done():
				result.Aborted = true
				result.AbortReason = "context cancelled"
				result.CompletedAt = time.Now().UTC()
				result.Duration = time.Since(result.StartedAt).Round(time.Second).String()
				return result, nil
			default:
			}

			step := RolloutStep{
				Replica:      i + 1,
				OldContainer: oldContainerID,
			}

			// Create a new container with the new image
			// Use deploy_to_node or create_container with the service template
			newContainerID, err := rm.createReplacement(ctx, svc, newImage)
			if err != nil {
				step.Status = "failed"
				result.Failed++
				result.History = append(result.History, step)
				if abortOnFailure {
					result.Aborted = true
					result.AbortReason = fmt.Sprintf("failed to create replacement for replica %d: %v", i+1, err)
					break
				}
				continue
			}
			step.NewContainer = newContainerID

			// Wait for health check to pass
			healthy := rm.waitForHealth(ctx, newContainerID, healthTimeout)
			if !healthy {
				step.Status = "failed"
				result.Failed++
				result.History = append(result.History, step)
				// Kill the failed new container
				_, _ = rm.backend.KillSandbox(newContainerID)
				if abortOnFailure {
					result.Aborted = true
					result.AbortReason = fmt.Sprintf("replica %d failed health check within %ds", i+1, healthWaitSeconds)
					break
				}
				continue
			}

			// New replica is healthy — kill the old one
			_, _ = rm.backend.KillSandbox(oldContainerID)
			step.Status = "healthy"
			result.Succeeded++
			result.History = append(result.History, step)

			// Update service registry
			svc.ContainerIDs[i] = newContainerID
		}

	case "blue-green":
		// Create all new replicas first
		newContainerIDs := make([]string, 0, len(oldContainers))
		allHealthy := true

		for i := 0; i < len(oldContainers); i++ {
			newID, err := rm.createReplacement(ctx, svc, newImage)
			if err != nil {
				result.Aborted = true
				result.AbortReason = fmt.Sprintf("failed to create blue-green replica %d: %v", i+1, err)
				allHealthy = false
				break
			}
			newContainerIDs = append(newContainerIDs, newID)
			step := RolloutStep{
				Replica:      i + 1,
				OldContainer: oldContainers[i],
				NewContainer: newID,
				Status:       "pending",
			}

			healthy := rm.waitForHealth(ctx, newID, healthTimeout)
			if !healthy {
				step.Status = "failed"
				result.Failed++
				result.History = append(result.History, step)
				allHealthy = false
				// Kill the failed container
				_, _ = rm.backend.KillSandbox(newID)
				break
			}
			step.Status = "healthy"
			step.HealthCheck = healthTimeout.String()
			result.Succeeded++
			result.History = append(result.History, step)
		}

		if allHealthy && len(newContainerIDs) == len(oldContainers) {
			// Switch traffic: kill all old containers
			for _, oldID := range oldContainers {
				_, _ = rm.backend.KillSandbox(oldID)
			}
			svc.ContainerIDs = newContainerIDs
		} else {
			// Rollback: kill all new containers
			for _, newID := range newContainerIDs {
				_, _ = rm.backend.KillSandbox(newID)
			}
			result.Aborted = true
			if result.AbortReason == "" {
				result.AbortReason = "blue-green: not all replicas became healthy"
			}
		}
	}

	// Update service with new container IDs
	_ = scaleMgr.saveService(svc)

	result.CompletedAt = time.Now().UTC()
	result.Duration = time.Since(result.StartedAt).Round(time.Second).String()

	return result, nil
}

// createReplacement creates a new container with the same config but new image.
func (rm *RolloutManager) createReplacement(ctx context.Context, svc *Service, newImage string) (string, error) {
	// Build ports slice from service port
	var ports []int
	if svc.Port > 0 {
		ports = []int{svc.Port}
	}

	// Create a new template from the new image (same ports as service template)
	templateResp, err := rm.backend.CreateTemplateFromImage(
		newImage,
		ports,
		0, // writable layer — use default
		nil,
		nil,
		"",
	)
	if err != nil {
		return "", fmt.Errorf("failed to create template from new image: %w", err)
	}

	templateID, ok := templateResp.(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("unexpected template creation response")
	}
	tID, _ := templateID["id"].(string)
	if tID == "" {
		return "", fmt.Errorf("template creation did not return an ID")
	}

	// Create and start the container
	containerResp, err := rm.backend.CreateSandbox(tID, svc.MemoryMB, svc.CPUCount, nil, map[string]interface{}{
		"service": svc.Name,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create new container: %w", err)
	}

	container, ok := containerResp.(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("unexpected container creation response")
	}
	cID, _ := container["id"].(string)
	if cID == "" {
		return "", fmt.Errorf("container creation did not return an ID")
	}

	return cID, nil
}

// waitForHealth polls the health check manager until the container is healthy or timeout.
func (rm *RolloutManager) waitForHealth(ctx context.Context, containerID string, timeout time.Duration) bool {
	// If there's no health check configured, wait a default grace period
	if healthMgr == nil {
		time.Sleep(3 * time.Second)
		return true
	}

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
		healthMgr.mu.Lock()
		check, exists := healthMgr.checks[containerID]
		healthMgr.mu.Unlock()
		if !exists {
			// No health check configured — wait a short grace period
			time.Sleep(3 * time.Second)
			return true
		}
		if check.LastStatus == "healthy" && check.ConsecutiveFailures == 0 {
			return true
		}
	}
	return false
}
