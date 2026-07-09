// Package main: cluster event stream and history.
//
// Records operational events (container starts/stops, deploys, scaling,
// health failures, alerts) in an in-memory ring buffer with disk persistence.
// The LLM uses events_list for "what changed recently?" and events_watch
// could be used for real-time monitoring via SSE (future).
//
// Unlike audit logs (which record WHO called WHAT tool), events record
// WHAT HAPPENED in the cluster — a semantic record of operational state
// changes.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---- Types ----

type EventType string

const (
	EventContainerStarted  EventType = "container.started"
	EventContainerStopped  EventType = "container.stopped"
	EventContainerCrashed  EventType = "container.crashed"
	EventDeployCompleted   EventType = "deploy.completed"
	EventDeployFailed      EventType = "deploy.failed"
	EventScaleChanged      EventType = "scale.changed"
	EventHealthFail        EventType = "health.failed"
	EventHealthRecovered   EventType = "health.recovered"
	EventAlertTriggered    EventType = "alert.triggered"
	EventNodeJoined        EventType = "node.joined"
	EventNodeLeft          EventType = "node.left"
	EventRolloutCompleted  EventType = "rollout.completed"
	EventRolloutFailed     EventType = "rollout.failed"
	EventBackupCompleted   EventType = "backup.completed"
	EventCertExpiring      EventType = "cert.expiring"
	EventDBCreated         EventType = "database.created"
	EventEnvPromoted       EventType = "environment.promoted"
)

type ClusterEvent struct {
	ID        string    `json:"id"`
	Type      EventType `json:"type"`
	Message   string    `json:"message"`
	Container string    `json:"container,omitempty"`
	Service   string    `json:"service,omitempty"`
	Node      string    `json:"node,omitempty"`
	Severity  string    `json:"severity"` // info, warning, critical
	Timestamp time.Time `json:"timestamp"`
}

type EventListResult struct {
	Events []ClusterEvent `json:"events"`
	Total  int            `json:"total"`
}

// ---- Manager ----

var eventMgr *EventManager

type EventManager struct {
	mu       sync.Mutex
	events   []ClusterEvent
	maxSize  int
	rootDir  string
}

func newEventManager() *EventManager {
	em := &EventManager{
		events:  make([]ClusterEvent, 0, 256),
		maxSize: 1000, // in-memory ring buffer
		rootDir: envOr("CUBE_EVENTS_ROOT", "/var/lib/cube-container/events"),
	}
	em.loadFromDisk()
	return em
}

// ---- Disk persistence (append-only daily file) ----

func (em *EventManager) eventFilePath() string {
	return filepath.Join(em.rootDir, fmt.Sprintf("events-%s.json", time.Now().UTC().Format("2006-01-02")))
}

func (em *EventManager) loadFromDisk() {
	// Load today's events
	data, err := os.ReadFile(em.eventFilePath())
	if err != nil {
		return
	}
	var events []ClusterEvent
	if err := json.Unmarshal(data, &events); err != nil {
		return
	}
	em.events = events
	if len(em.events) > em.maxSize {
		em.events = em.events[len(em.events)-em.maxSize:]
	}
}

func (em *EventManager) persistEvent(event ClusterEvent) {
	if err := os.MkdirAll(em.rootDir, 0700); err != nil {
		return
	}
	// Append to daily file
	data, err := json.MarshalIndent(event, "", "  ")
	if err != nil {
		return
	}
	f, err := os.OpenFile(em.eventFilePath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(data)
	f.Write([]byte("\n"))
}

// ---- Operations ----

// Record adds an event to the stream.
func (em *EventManager) Record(eventType EventType, message, container, service, node, severity string) {
	if em == nil {
		return
	}
	em.mu.Lock()
	defer em.mu.Unlock()

	event := ClusterEvent{
		ID:        generateID("evt"),
		Type:      eventType,
		Message:   message,
		Container: container,
		Service:   service,
		Node:      node,
		Severity:  severity,
		Timestamp: time.Now().UTC(),
	}

	em.events = append(em.events, event)
	if len(em.events) > em.maxSize {
		em.events = em.events[len(em.events)-em.maxSize:]
	}

	// Persist (fire and forget — don't block event recording)
	go em.persistEvent(event)
}

// List returns events filtered by type, severity, or time range.
func (em *EventManager) List(eventType, severity string, sinceMinutes, limit int) (*EventListResult, error) {
	em.mu.Lock()
	defer em.mu.Unlock()

	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	var cutoff time.Time
	if sinceMinutes > 0 {
		cutoff = time.Now().UTC().Add(time.Duration(-sinceMinutes) * time.Minute)
	}

	var filtered []ClusterEvent
	for _, e := range em.events {
		if eventType != "" && e.Type != EventType(eventType) {
			continue
		}
		if severity != "" && e.Severity != severity {
			continue
		}
		if sinceMinutes > 0 && e.Timestamp.Before(cutoff) {
			continue
		}
		filtered = append(filtered, e)
	}

	// Sort by timestamp descending (most recent first)
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Timestamp.After(filtered[j].Timestamp)
	})

	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	return &EventListResult{
		Events: filtered,
		Total:  len(filtered),
	}, nil
}

// ---- Helpers ----

var _ = strings.TrimSpace
