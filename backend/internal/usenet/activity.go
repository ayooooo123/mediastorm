package usenet

import (
	"sort"
	"strings"
	"sync"
	"time"
)

type ActivityUsage struct {
	ID             int64     `json:"id"`
	Label          string    `json:"label"`
	StartedAt      time.Time `json:"startedAt"`
	Segments       int       `json:"segments"`
	BytesRead      int64     `json:"bytesRead"`
	EstimatedBytes int64     `json:"estimatedBytes"`
	MaxWorkers     int       `json:"maxWorkers"`
	CurrentSegment int       `json:"currentSegment"`
	Completed      bool      `json:"completed"`
}

type ActivitySnapshot struct {
	ActiveReaders     int64           `json:"activeReaders"`
	ActiveSegments    int64           `json:"activeSegments"`
	EstimatedMemoryMB int64           `json:"estimatedMemoryMB"`
	Usages            []ActivityUsage `json:"usages"`
}

var activityRegistry = struct {
	sync.RWMutex
	usages map[int64]ActivityUsage
}{usages: make(map[int64]ActivityUsage)}

func registerActivity(usage ActivityUsage) {
	if strings.TrimSpace(usage.Label) == "" {
		usage.Label = "Usenet stream"
	}
	if usage.StartedAt.IsZero() {
		usage.StartedAt = time.Now().UTC()
	}

	activityRegistry.Lock()
	activityRegistry.usages[usage.ID] = usage
	activityRegistry.Unlock()
}

func updateActivity(id int64, fn func(*ActivityUsage)) {
	activityRegistry.Lock()
	if usage, ok := activityRegistry.usages[id]; ok {
		fn(&usage)
		activityRegistry.usages[id] = usage
	}
	activityRegistry.Unlock()
}

func unregisterActivity(id int64) {
	activityRegistry.Lock()
	delete(activityRegistry.usages, id)
	activityRegistry.Unlock()
}

func ActivityStats() ActivitySnapshot {
	readers, segments, estMemoryMB := GlobalReaderStats()

	activityRegistry.RLock()
	usages := make([]ActivityUsage, 0, len(activityRegistry.usages))
	for _, usage := range activityRegistry.usages {
		usages = append(usages, usage)
	}
	activityRegistry.RUnlock()

	sort.Slice(usages, func(i, j int) bool {
		return usages[i].StartedAt.Before(usages[j].StartedAt)
	})

	return ActivitySnapshot{
		ActiveReaders:     readers,
		ActiveSegments:    segments,
		EstimatedMemoryMB: estMemoryMB,
		Usages:            usages,
	}
}
