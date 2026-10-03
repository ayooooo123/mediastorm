package epg

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"novastream/models"
)

func TestHDHomeRunPartialGuideDiagnostics(t *testing.T) {
	var logs bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousOutput) })
	now := time.Now().UTC()
	tuner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"DeviceAuth":"private-token"}`)
	}))
	defer tuner.Close()
	guide := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<tv>
<channel id="available"><display-name>5.1</display-name><display-name>TESTTV</display-name></channel>
<channel id="missing"><display-name>EMPTY</display-name></channel>
<channel id="invalid"><display-name>BADTIME</display-name></channel>
<programme channel="available" start="%s" stop="%s"><title>Private title</title></programme>
<programme channel="invalid" start="bad" stop="bad"><title>Private title</title></programme>
<programme channel="invalid" start="%s" stop="bad"><title>Private title</title></programme>
</tv>`, now.Add(-time.Hour).Format("20060102150405 -0700"), now.Add(time.Hour).Format("20060102150405 -0700"), now.Format("20060102150405 -0700"))
	}))
	defer guide.Close()
	service := &Service{client: tuner.Client(), hdHomeRunGuideURL: guide.URL}
	schedule := &models.EPGSchedule{Channels: map[string]models.EPGChannel{}, Programs: map[string][]models.EPGProgram{}}
	if err := service.downloadHDHomeRunGuide(context.Background(), tuner.URL+"/discover.json", schedule); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"channelMetadata=3 programChannels=1 programs=1 withoutPrograms=2 withCurrent=1 withFuture=0 invalidTimes=2", `id="invalid" name="BADTIME" aliases=["BADTIME"] metadataPresent=true programs=0 current=0 future=0 invalidTimes=2`, `id="missing" name="EMPTY"`, `id="available" name="5.1" aliases=["5.1" "TESTTV"]`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("missing diagnostic %q in %s", want, logs.String())
		}
	}
	if strings.Contains(logs.String(), "private-token") || strings.Contains(logs.String(), "Private title") || strings.Contains(logs.String(), "DeviceAuth=") {
		t.Fatal("diagnostics leaked authentication or programme contents")
	}
}

func TestHDHomeRunCoverageDetailsAreBoundedAndMissingFirst(t *testing.T) {
	var logs bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousOutput) })
	now := time.Now().UTC()
	schedule := &models.EPGSchedule{Channels: map[string]models.EPGChannel{}, Programs: map[string][]models.EPGProgram{}}
	for i := 0; i < 105; i++ {
		id := fmt.Sprintf("channel-%03d", i)
		schedule.Channels[id] = models.EPGChannel{ID: id}
		schedule.Programs[id] = []models.EPGProgram{{Start: now, Stop: now.Add(time.Hour)}}
	}
	schedule.Channels["zzz-missing"] = models.EPGChannel{ID: "zzz-missing"}
	logHDHomeRunGuideCoverage("tuner", "download", schedule, nil, now)
	output := logs.String()
	if strings.Count(output, "[hdhomerun-epg] channel ") != 100 || !strings.Contains(output, "omittedDetails=6") {
		t.Fatal("channel diagnostics were not bounded")
	}
	if strings.Index(output, `id="zzz-missing"`) > strings.Index(output, `id="channel-000"`) || !strings.Contains(output, `id="zzz-missing"`) {
		t.Fatal("missing channel was not prioritized")
	}
}
