package downloader

import (
	"fmt"
	"sync"

	downloadtask "EasyDownload/internal/download/task"
)

type PlatformRegistry struct {
	mu       sync.RWMutex
	adapters map[downloadtask.PlatformID]downloadtask.PlatformAdapter
}

func NewPlatformRegistry() *PlatformRegistry {
	return &PlatformRegistry{adapters: make(map[downloadtask.PlatformID]downloadtask.PlatformAdapter)}
}

func (r *PlatformRegistry) Register(adapter downloadtask.PlatformAdapter) error {
	if adapter == nil {
		return fmt.Errorf("platform adapter is nil")
	}
	id := adapter.ID()
	if id == "" {
		return fmt.Errorf("platform adapter id is empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.adapters[id]; exists {
		return fmt.Errorf("platform adapter %q is already registered", id)
	}
	r.adapters[id] = adapter
	return nil
}

func (r *PlatformRegistry) Get(id downloadtask.PlatformID) (downloadtask.PlatformAdapter, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	adapter, ok := r.adapters[id]
	return adapter, ok
}
