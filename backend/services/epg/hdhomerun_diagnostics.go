package epg

import (
	"log"
	"sort"
	"time"

	"novastream/models"
)

func hdHomeRunChannelCount(schedule *models.EPGSchedule) int {
	if schedule == nil {
		return 0
	}
	return len(schedule.Channels)
}

func hdHomeRunGuideUpdated(schedule *models.EPGSchedule) string {
	if schedule == nil || schedule.LastUpdated.IsZero() {
		return ""
	}
	return schedule.LastUpdated.UTC().Format(time.RFC3339)
}

// Log metadata and coverage only: never the authenticated request URL, response
// body, stream URLs, or programme titles. Missing channels are logged first and
// details are bounded so a large cable lineup cannot flood the backend log.
func logHDHomeRunGuideCoverage(tuner, phase string, schedule *models.EPGSchedule, invalidTimes map[string]int, now time.Time) {
	if schedule == nil {
		return
	}
	ids := make(map[string]bool, len(schedule.Channels))
	for id := range schedule.Channels {
		ids[id] = true
	}
	for id := range schedule.Programs {
		ids[id] = true
	}
	for id := range invalidTimes {
		ids[id] = true
	}
	ordered := make([]string, 0, len(ids))
	withPrograms, withCurrent, withFuture, invalidTotal := 0, 0, 0, 0
	for id := range ids {
		ordered = append(ordered, id)
		invalidTotal += invalidTimes[id]
		if len(schedule.Programs[id]) > 0 {
			withPrograms++
		}
		current, future := false, false
		for _, program := range schedule.Programs[id] {
			current = current || (!program.Start.After(now) && program.Stop.After(now))
			future = future || program.Start.After(now)
		}
		if current {
			withCurrent++
		}
		if future {
			withFuture++
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		iMissing := len(schedule.Programs[ordered[i]]) == 0
		jMissing := len(schedule.Programs[ordered[j]]) == 0
		if iMissing != jMissing {
			return iMissing
		}
		return ordered[i] < ordered[j]
	})
	const detailLimit = 100
	log.Printf("[hdhomerun-epg] coverage tuner=%q phase=%q channelMetadata=%d programChannels=%d programs=%d withoutPrograms=%d withCurrent=%d withFuture=%d invalidTimes=%d now=%s omittedDetails=%d",
		tuner, phase, len(schedule.Channels), withPrograms, countSchedulePrograms(schedule), len(ids)-withPrograms, withCurrent, withFuture, invalidTotal, now.Format(time.RFC3339), max(0, len(ordered)-detailLimit))
	for i, id := range ordered {
		if i >= detailLimit {
			break
		}
		channel, metadataPresent := schedule.Channels[id]
		programs := schedule.Programs[id]
		var first, last time.Time
		current, future := 0, 0
		for _, program := range programs {
			if first.IsZero() || program.Start.Before(first) {
				first = program.Start
			}
			if program.Stop.After(last) {
				last = program.Stop
			}
			if !program.Start.After(now) && program.Stop.After(now) {
				current++
			}
			if program.Start.After(now) {
				future++
			}
		}
		log.Printf("[hdhomerun-epg] channel tuner=%q phase=%q id=%q name=%q aliases=%q metadataPresent=%v programs=%d current=%d future=%d invalidTimes=%d firstStart=%s lastStop=%s",
			tuner, phase, id, channel.Name, channel.Aliases, metadataPresent, len(programs), current, future, invalidTimes[id], first.UTC().Format(time.RFC3339), last.UTC().Format(time.RFC3339))
	}
}
