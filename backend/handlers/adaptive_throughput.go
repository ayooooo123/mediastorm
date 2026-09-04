package handlers

import (
	"log"
	"net/http"
	"strconv"
	"strings"

	"novastream/models"
)

const (
	adaptiveThroughputMbpsHeader       = "X-Adaptive-Throughput-Mbps"
	adaptiveThroughputMeasuredAtHeader = "X-Adaptive-Throughput-Measured-At"
)

func adaptiveThroughputFromRequest(r *http.Request) *models.AdaptiveThroughputContext {
	rawMbps := strings.TrimSpace(r.Header.Get(adaptiveThroughputMbpsHeader))
	rawMeasuredAt := strings.TrimSpace(r.Header.Get(adaptiveThroughputMeasuredAtHeader))
	if rawMbps == "" && rawMeasuredAt == "" {
		return nil
	}
	mbps, mbpsErr := strconv.ParseFloat(rawMbps, 64)
	measuredAt, measuredAtErr := strconv.ParseInt(rawMeasuredAt, 10, 64)
	if mbpsErr != nil || measuredAtErr != nil || mbps <= 0 || measuredAt <= 0 {
		log.Printf("[adaptive] ignored invalid request throughput context mbps=%q measuredAt=%q", rawMbps, rawMeasuredAt)
		return nil
	}
	log.Printf("[adaptive] request throughput context mbps=%.1f measuredAt=%d", mbps, measuredAt)
	return &models.AdaptiveThroughputContext{MeasuredMbps: mbps, MeasuredAt: measuredAt}
}
