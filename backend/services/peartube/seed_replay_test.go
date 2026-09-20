package peartube

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// contributeStub answers /acquisitions/contribute from a key→job table and
// records what was submitted. A /source-grants call is a test failure: these
// cases are exactly the ones where no grant may be issued or revoked.
type contributeStub struct {
	t       *testing.T
	jobs    map[string]string // idempotency key -> `{"acquisitionId":…,"state":…}`
	fallback string
	keys    []string
	attach  int
}

func (s *contributeStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != companionAPIPrefix+"/acquisitions/contribute" {
		s.attach++
		w.WriteHeader(http.StatusOK)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.t.Fatalf("read contribute body: %v", err)
	}
	var submitted ContributeAcquisitionRequest
	if err := json.Unmarshal(body, &submitted); err != nil {
		s.t.Fatalf("decode contribute body: %v", err)
	}
	s.keys = append(s.keys, submitted.IdempotencyKey)
	acquisition, known := s.jobs[submitted.IdempotencyKey]
	if !known {
		acquisition = s.fallback
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"acquisition":` + acquisition + `}`))
}

// A successor dies too. Once the first replacement has failed, a walk that
// stopped after one rotation would replay the original and that replacement
// forever and never reach the third job, so the release could never archive
// again. The chain has to be followed to the first job that can take a grant.
func TestContributeFollowsChainOfDeadJobsAcrossTwoLifecycles(t *testing.T) {
	const firstKey = "mediastorm-v2_first"
	secondKey := rotatedSeedIdempotencyKey(firstKey, "acq_first")
	thirdKey := rotatedSeedIdempotencyKey(secondKey, "acq_second")

	stub := &contributeStub{
		t: t,
		jobs: map[string]string{
			firstKey:  `{"acquisitionId":"acq_first","state":"failed"}`,
			secondKey: `{"acquisitionId":"acq_second","state":"failed"}`,
			thirdKey:  `{"acquisitionId":"acq_third","state":"queued"}`,
		},
		fallback: `{"acquisitionId":"acq_unexpected","state":"queued"}`,
	}
	client := newCompanionSearchClient(t, stub.ServeHTTP)

	job, err := client.contributeFreshAcquisition(context.Background(), ContributeAcquisitionRequest{
		IdempotencyKey: firstKey,
		Title:          "Night at the Museum",
		Selector:       ContributeAcquisitionSelector{Kind: "movie", Namespace: "tmdb", Identifier: "1593"},
	})
	if err != nil {
		t.Fatalf("contributeFreshAcquisition: %v", err)
	}
	if job.JobID != "acq_third" || job.Status != "queued" {
		t.Fatalf("job = %+v, want the third job acq_third queued", job)
	}
	want := []string{firstKey, secondKey, thirdKey}
	if len(stub.keys) != len(want) {
		t.Fatalf("submitted keys = %v, want the full chain %v", stub.keys, want)
	}
	for i := range want {
		if stub.keys[i] != want[i] {
			t.Fatalf("submitted key [%d] = %q, want %q", i, stub.keys[i], want[i])
		}
	}
}

// Five dead generations is not a pathological relay, it is a release that has
// failed five times over its life — and every one of those jobs stays in the
// relay's history. A hop limit low enough to stop here would put a ceiling on
// the release's lifetime instead of on load: every later submission would walk
// the whole history and stop one short of the generation that can still take a
// grant, which is the wedge this walk exists to remove.
func TestContributeReachesLiveGenerationPastFiveDeadOnes(t *testing.T) {
	const firstKey = "mediastorm-v2_generation"
	jobs := map[string]string{}
	key := firstKey
	var wantKeys []string
	for generation := 1; generation <= 5; generation++ {
		id := fmt.Sprintf("acq_gen%d", generation)
		jobs[key] = fmt.Sprintf(`{"acquisitionId":%q,"state":"failed"}`, id)
		wantKeys = append(wantKeys, key)
		key = rotatedSeedIdempotencyKey(key, id)
	}
	jobs[key] = `{"acquisitionId":"acq_live","state":"queued"}`
	wantKeys = append(wantKeys, key)

	stub := &contributeStub{t: t, jobs: jobs, fallback: `{"acquisitionId":"acq_unexpected","state":"queued"}`}
	client := newCompanionSearchClient(t, stub.ServeHTTP)

	job, err := client.contributeFreshAcquisition(context.Background(), ContributeAcquisitionRequest{
		IdempotencyKey: firstKey,
		Title:          "Night at the Museum",
		Selector:       ContributeAcquisitionSelector{Kind: "movie", Namespace: "tmdb", Identifier: "1593"},
	})
	if err != nil {
		t.Fatalf("contributeFreshAcquisition: %v", err)
	}
	if job.JobID != "acq_live" || job.Status != "queued" {
		t.Fatalf("job = %+v, want the live generation acq_live queued", job)
	}
	if len(stub.keys) != len(wantKeys) {
		t.Fatalf("submitted %d keys, want %d (five dead generations then the live one)", len(stub.keys), len(wantKeys))
	}
	for i := range wantKeys {
		if stub.keys[i] != wantKeys[i] {
			t.Fatalf("submitted key [%d] = %q, want %q", i, stub.keys[i], wantKeys[i])
		}
	}
}

// Storm protection is the cycle check, not the depth limit: a relay that keeps
// answering with a job this walk already retired is not advancing, and the next
// rotation would be the first submission that creates anything.
func TestContributeStopsWhenTheChainDoesNotAdvance(t *testing.T) {
	stub := &contributeStub{
		t:        t,
		jobs:     map[string]string{},
		fallback: `{"acquisitionId":"acq_same","state":"failed"}`,
	}
	client := newCompanionSearchClient(t, stub.ServeHTTP)

	_, err := client.contributeFreshAcquisition(context.Background(), ContributeAcquisitionRequest{
		IdempotencyKey: "mediastorm-v2_same_corpse",
		Title:          "Night at the Museum",
		Selector:       ContributeAcquisitionSelector{Kind: "movie", Namespace: "tmdb", Identifier: "1593"},
	})
	if err == nil {
		t.Fatal("expected an error when the relay keeps returning the same retired job")
	}
	// The original, then exactly one rotation that proves the chain is stuck.
	if got := len(stub.keys); got != 2 {
		t.Fatalf("submissions = %d, want 2; a stuck chain must not keep submitting", got)
	}
}

// Replaying a title whose archive is already running must leave that archive
// alone. Only a queued job accepts a grant, so issuing a second capability and
// attaching it would be refused — and the refusal path revokes every capability
// for the job, starving the transfer that was working.
func TestReplayOfRunningArchiveKeepsItsCapability(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	registry := newTestRemoteRegistry(t, clock, 1<<20)
	secret := testSourceSecret()

	content := make([]byte, 4096)
	if _, err := rand.Read(content); err != nil {
		t.Fatalf("seed content: %v", err)
	}
	reader := &fakeRemoteReader{content: content, addressLife: 8}
	live := issueTestRemoteSource(t, registry, reader, "/live/archive.mkv", "acq_live", now.Add(20*time.Minute))

	stub := &contributeStub{
		t:       t,
		jobs:    map[string]string{},
		fallback: `{"acquisitionId":"acq_live","state":"acquiring"}`,
	}
	client := newCompanionSearchClient(t, stub.ServeHTTP)

	prepared, err := registry.PrepareRemote(context.Background(), RemoteSource{Reader: reader, StreamPath: "/live/archive.mkv"}, "video/x-matroska")
	if err != nil {
		t.Fatalf("PrepareRemote: %v", err)
	}
	defer prepared.Close()

	job, err := client.submitGrantedIngest(context.Background(), registry, prepared, grantedIngestSubmission{
		IdempotencyKey: "mediastorm-v2_live",
		Coordinates:    ArchiveCoordinates{ContentKind: "movie", TMDBID: "1593", TMDBTitle: "Night at the Museum"},
		SourcePath:     "/live/archive.mkv",
	})
	if err != nil {
		t.Fatalf("submitGrantedIngest: %v", err)
	}
	if job.JobID != "acq_live" || job.Status != "acquiring" {
		t.Fatalf("job = %+v, want the running job reported unchanged", job)
	}
	if stub.attach != 0 {
		t.Fatalf("attached a grant %d time(s) to a job that cannot take one", stub.attach)
	}

	// The capability the running archive reads through must still serve.
	now = now.Add(2 * time.Minute)
	byteRange := fmt.Sprintf("bytes=%d-%d", 0, 511)
	request := signedSourceRequest(t, http.MethodGet, live.Capability, "acq_live", live.ETag,
		byteRange, "peartube-companion", secret, now, "nonce-replay-"+strconv.Itoa(1))
	response := serveSource(registry, request)
	if response.Code != http.StatusPartialContent {
		t.Fatalf("range after replay = %d, body = %s; the running archive lost its capability",
			response.Code, response.Body.String())
	}
}
