package handlers

import (
	"net/http/httptest"
	"testing"
)

func TestAdaptiveThroughputFromRequest(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/indexers/search", nil)
	req.Header.Set(adaptiveThroughputMbpsHeader, "938.1")
	req.Header.Set(adaptiveThroughputMeasuredAtHeader, "1700000000")
	got := adaptiveThroughputFromRequest(req)
	if got == nil || got.MeasuredMbps != 938.1 || got.MeasuredAt != 1700000000 {
		t.Fatalf("context = %+v", got)
	}
}

func TestAdaptiveThroughputFromRequestRejectsPartialOrInvalidContext(t *testing.T) {
	for _, headers := range [][2]string{{"938.1", ""}, {"bad", "1700000000"}, {"0", "1700000000"}} {
		req := httptest.NewRequest("GET", "/api/indexers/search", nil)
		req.Header.Set(adaptiveThroughputMbpsHeader, headers[0])
		req.Header.Set(adaptiveThroughputMeasuredAtHeader, headers[1])
		if got := adaptiveThroughputFromRequest(req); got != nil {
			t.Fatalf("headers %q produced context %+v", headers, got)
		}
	}
}
