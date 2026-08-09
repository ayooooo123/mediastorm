package indexer

import (
	"strings"
	"testing"

	"novastream/config"
	"novastream/models"
	"novastream/utils/filter"
)

func TestScoreResult_ServicePriority(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingServicePriority, Name: "Service Priority", Enabled: true, Order: 0},
		},
		ServicePriority: config.StreamingServicePriorityUsenet,
	}

	usenet := models.NZBResult{Title: "Test", ServiceType: models.ServiceTypeUsenet}
	debrid := models.NZBResult{Title: "Test", ServiceType: models.ServiceTypeDebrid}

	scoreU, _ := ScoreResult(usenet, ctx)
	scoreD, _ := ScoreResult(debrid, ctx)

	if scoreU <= scoreD {
		t.Fatalf("expected usenet score (%d) > debrid score (%d) when usenet is preferred", scoreU, scoreD)
	}
}

func TestScoreResult_PreferredTerms(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingPreferredTerms, Name: "Preferred Terms", Enabled: true, Order: 0},
		},
		PreferredTerms: filter.CompileTerms([]string{"remux"}),
	}

	with := models.NZBResult{Title: "Movie 2024 Remux 1080p"}
	without := models.NZBResult{Title: "Movie 2024 BluRay 1080p"}

	scoreWith, _ := ScoreResult(with, ctx)
	scoreWithout, _ := ScoreResult(without, ctx)

	if scoreWith <= scoreWithout {
		t.Fatalf("expected preferred term match (%d) > no match (%d)", scoreWith, scoreWithout)
	}
}

func TestScoreResult_NonPreferredTerms(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingNonPreferredTerms, Name: "Non-Preferred Terms", Enabled: true, Order: 0},
		},
		NonPreferredTerms: filter.CompileTerms([]string{"cam"}),
	}

	cam := models.NZBResult{Title: "Movie 2024 CAM"}
	bluray := models.NZBResult{Title: "Movie 2024 BluRay"}

	scoreCam, _ := ScoreResult(cam, ctx)
	scoreBluray, _ := ScoreResult(bluray, ctx)

	if scoreCam >= scoreBluray {
		t.Fatalf("expected non-preferred term (%d) < no match (%d)", scoreCam, scoreBluray)
	}
	if scoreCam >= 0 {
		t.Fatalf("expected negative score for non-preferred match, got %d", scoreCam)
	}
}

func TestScoreResult_Resolution(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 0},
		},
	}

	r4k := models.NZBResult{Title: "Movie 2160p"}
	r1080 := models.NZBResult{Title: "Movie 1080p"}
	r720 := models.NZBResult{Title: "Movie 720p"}

	s4k, _ := ScoreResult(r4k, ctx)
	s1080, _ := ScoreResult(r1080, ctx)
	s720, _ := ScoreResult(r720, ctx)

	if s4k <= s1080 || s1080 <= s720 {
		t.Fatalf("expected 4k(%d) > 1080p(%d) > 720p(%d)", s4k, s1080, s720)
	}
}

func TestScoreResult_HigherPriorityCriterionDominates(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 0},
			{ID: config.RankingLanguage, Name: "Language", Enabled: true, Order: 1},
		},
		PreferredLang: "eng",
	}

	r720English := models.NZBResult{
		Title:      "Movie 720p",
		Attributes: map[string]string{"languages": "eng"},
	}
	r2160Unknown := models.NZBResult{Title: "Movie 2160p"}

	_, breakdown := ScoreResult(r720English, ctx)

	results := []models.NZBResult{r720English, r2160Unknown}
	(&Service{}).sortResultsByScore(results, ctx)
	if results[0].Title != "Movie 2160p" {
		t.Fatalf("expected 2160p without language to rank above 720p with language, got %q", results[0].Title)
	}

	// Language sits one display-score band below resolution.
	wantLang := levelMax * scoreBand
	for _, item := range breakdown {
		if item.Criterion == "Language" {
			if item.Points != wantLang {
				t.Fatalf("language points = %d, want %d", item.Points, wantLang)
			}
			return
		}
	}
	t.Fatal("expected language breakdown item")
}

func TestScoreResult_Size(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingSize, Name: "Size", Enabled: true, Order: 0},
		},
	}

	big := models.NZBResult{Title: "Movie", SizeBytes: 10 * 1024 * 1024 * 1024}  // 10GB
	small := models.NZBResult{Title: "Movie", SizeBytes: 1 * 1024 * 1024 * 1024} // 1GB

	sBig, _ := ScoreResult(big, ctx)
	sSmall, _ := ScoreResult(small, ctx)

	if sBig <= sSmall {
		t.Fatalf("expected bigger file (%d) > smaller file (%d)", sBig, sSmall)
	}
}

func TestScoreResult_SizeTopPriorityBeatsHigherResolution(t *testing.T) {
	// With File Size as the #1 criterion, the largest file must win even when a
	// smaller file has higher resolution (the bug: a 35GB 4K file outranking a
	// 50GB 1080p file when size was supposedly top priority).
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingSize, Name: "File Size", Enabled: true, Order: 0},
			{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 1},
		},
	}

	big1080 := models.NZBResult{Title: "Movie 1080p", SizeBytes: 50 * 1024 * 1024 * 1024}
	small4k := models.NZBResult{Title: "Movie 2160p", SizeBytes: 35 * 1024 * 1024 * 1024}

	results := []models.NZBResult{small4k, big1080}
	(&Service{}).sortResultsByScore(results, ctx)
	if results[0].Title != big1080.Title {
		t.Fatalf("expected larger 1080p file to rank above smaller 4K file when size is top priority, got %q", results[0].Title)
	}
}

func TestScoreResult_LowerPriorityBreaksTie(t *testing.T) {
	// When the top criterion (size) is equal, the lower criterion (resolution)
	// must break the tie.
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingSize, Name: "File Size", Enabled: true, Order: 0},
			{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 1},
		},
	}

	sameSize := int64(40 * 1024 * 1024 * 1024)
	r4k := models.NZBResult{Title: "Movie 2160p", SizeBytes: sameSize}
	r1080 := models.NZBResult{Title: "Movie 1080p", SizeBytes: sameSize}

	s4k, _ := ScoreResult(r4k, ctx)
	s1080, _ := ScoreResult(r1080, ctx)

	if s4k <= s1080 {
		t.Fatalf("expected 4K (%d) > 1080p (%d) as tiebreaker when size is equal", s4k, s1080)
	}
}

func TestScoreResult_DisabledCriteria(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingServicePriority, Name: "Service Priority", Enabled: false, Order: 0},
		},
		ServicePriority: config.StreamingServicePriorityUsenet,
	}

	usenet := models.NZBResult{Title: "Test", ServiceType: models.ServiceTypeUsenet}
	score, breakdown := ScoreResult(usenet, ctx)

	if score != 0 {
		t.Fatalf("expected 0 score with disabled criterion, got %d", score)
	}
	if len(breakdown) != 0 {
		t.Fatalf("expected no breakdown items with disabled criterion, got %d", len(breakdown))
	}
}

func TestScoreResult_YearMatchBreakdownIsDiagnosticOnly(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{},
	}

	with := models.NZBResult{Title: "Test", Attributes: map[string]string{"yearMatch": "true"}}

	score, breakdown := ScoreResult(with, ctx)
	if score != 0 {
		t.Fatalf("expected year match priority gate not to add score points, got %d", score)
	}
	if len(breakdown) != 1 {
		t.Fatalf("expected one year match breakdown item, got %d", len(breakdown))
	}
	if breakdown[0].Criterion != "Year Match" || breakdown[0].Points != 0 {
		t.Fatalf("unexpected year match breakdown: %+v", breakdown[0])
	}
	if strings.Contains(breakdown[0].Reason, "priority gate") {
		t.Fatalf("did not expect priority gate reason, got %q", breakdown[0].Reason)
	}
}

func TestScoreResult_MissingYearHasNoBreakdown(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{},
	}

	without := models.NZBResult{Title: "Test", Attributes: map[string]string{}}

	score, breakdown := ScoreResult(without, ctx)
	if score != 0 {
		t.Fatalf("expected missing year not to add score points, got %d", score)
	}
	if len(breakdown) != 0 {
		t.Fatalf("expected missing year to have no breakdown item, got %+v", breakdown)
	}
}

func TestSortResultsByScore_MissingYearIsNeutral(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 0},
			{ID: config.RankingSize, Name: "Size", Enabled: true, Order: 1},
		},
	}

	confirmedYear := models.NZBResult{
		Title:      "Wacky Races 1968 S01 DVDRip",
		SizeBytes:  2 * 1024 * 1024 * 1024,
		Attributes: map[string]string{"yearMatch": "true"},
	}
	noParsedYear := models.NZBResult{
		Title:      "Wacky Races Complete TV Series 2160p REMASTERED",
		SizeBytes:  100 * 1024 * 1024 * 1024,
		Attributes: map[string]string{},
	}

	results := []models.NZBResult{noParsedYear, confirmedYear}
	(&Service{}).sortResultsByScore(results, ctx)
	if results[0].Title != noParsedYear.Title {
		t.Fatalf("expected configured quality criteria to outrank year presence, got %q", results[0].Title)
	}
}

func TestSortResultsByScore_UsesEffectivePackItemSize(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingSize, Name: "Size", Enabled: true, Order: 0},
		},
	}

	pack := models.NZBResult{
		Title:        "Show S01 1080p WEB-DL",
		SizeBytes:    90 * 1024 * 1024 * 1024,
		EpisodeCount: 9,
	}
	single := models.NZBResult{
		Title:     "Show S01E01 1080p WEB-DL",
		SizeBytes: 12 * 1024 * 1024 * 1024,
	}

	results := []models.NZBResult{pack, single}
	(&Service{}).sortResultsByScore(results, ctx)
	if results[0].Title != single.Title {
		t.Fatalf("expected 12 GB episode to outrank pack estimated at 10 GB/item, got %q", results[0].Title)
	}

	_, breakdown := ScoreResult(pack, ctx)
	if len(breakdown) != 1 || !strings.Contains(breakdown[0].Reason, "10.0 GB per item") {
		t.Fatalf("expected per-item size breakdown, got %+v", breakdown)
	}
}

func TestSortResultsByScore_YearMatchAloneDoesNotSupersedeCriteria(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 0},
			{ID: config.RankingSize, Name: "Size", Enabled: true, Order: 1},
		},
	}

	looseYearMatch := models.NZBResult{
		Title:      "Wacky Races Forever 1968 S01 DVDRip",
		SizeBytes:  2 * 1024 * 1024 * 1024,
		Attributes: map[string]string{"yearMatch": "true"},
	}
	strongNoParsedYear := models.NZBResult{
		Title:      "Wacky Races Complete TV Series 2160p REMASTERED",
		SizeBytes:  100 * 1024 * 1024 * 1024,
		Attributes: map[string]string{},
	}

	results := []models.NZBResult{looseYearMatch, strongNoParsedYear}
	(&Service{}).sortResultsByScore(results, ctx)
	if results[0].Title != strongNoParsedYear.Title {
		t.Fatalf("expected weak title year match not to override normal ranking, got %q", results[0].Title)
	}
}

func TestSortResultsByScore_TargetEpisodeYearMatchDoesNotSupersedeCriteriaWithoutConflict(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 0},
			{ID: config.RankingSize, Name: "Size", Enabled: true, Order: 1},
		},
	}

	matchingYearTitle := "Little.House.On.The.Prairie.1974.S01E01.720p.BluRay.x264"
	results := filter.Results([]models.NZBResult{
		{Title: "Little.House.On.The.Prairie.S01E01.2160p.BluRay.x265", SizeBytes: 20 * 1024 * 1024 * 1024},
		{Title: matchingYearTitle, SizeBytes: 2 * 1024 * 1024 * 1024},
		{Title: "Little.House.On.The.Prairie.2026.S01E01.1080p.WEB-DL.x264", SizeBytes: 4 * 1024 * 1024 * 1024},
	}, filter.Options{
		ExpectedTitle: "Little House on the Prairie",
		ExpectedYear:  1974,
		IsMovie:       false,
		TargetSeason:  1,
		TargetEpisode: 1,
	})
	if len(results) != 2 {
		t.Fatalf("expected the yearless and matching-year results to pass while the conflict is filtered, got %d", len(results))
	}
	applyEpisodeYearPriority(results, 1974, 1974)
	(&Service{}).sortResultsByScore(results, ctx)
	if results[0].Title == matchingYearTitle {
		t.Fatalf("expected normal quality ranking without a surviving conflicting year, got %q", results[0].Title)
	}
}

func TestSortResultsByScore_TargetEpisodeYearMatchSupersedesCriteriaWithConflict(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 0},
			{ID: config.RankingSize, Name: "Size", Enabled: true, Order: 1},
		},
	}

	matchingYearTitle := "Little.House.On.The.Prairie.1974.S01E01.720p.BluRay.x264"
	results := []models.NZBResult{
		{Title: "Little.House.On.The.Prairie.S01E01.2160p.BluRay.x265", SizeBytes: 20 * 1024 * 1024 * 1024},
		{
			Title:      matchingYearTitle,
			SizeBytes:  2 * 1024 * 1024 * 1024,
			Attributes: map[string]string{"episodeYearMatch": "true", "episodeReleaseYear": "1974"},
		},
		{
			Title:      "Little.House.On.The.Prairie.2026.S01E01.1080p.WEB-DL.x264",
			SizeBytes:  4 * 1024 * 1024 * 1024,
			Attributes: map[string]string{"episodeReleaseYear": "2026"},
		},
	}

	applyEpisodeYearPriority(results, 1974, 1974)
	(&Service{}).sortResultsByScore(results, ctx)
	if results[0].Title != matchingYearTitle {
		t.Fatalf("expected matching series year to receive priority with a surviving conflict, got %q", results[0].Title)
	}
}

func TestApplyEpisodeYearPriority_EpisodeAirYearIsNotConflict(t *testing.T) {
	results := filter.Results([]models.NZBResult{
		{Title: "The.Office.2005.S04E01.720p.WEB-DL.x264", SizeBytes: 2 * 1024 * 1024 * 1024},
		{Title: "The.Office.2009.S04E01.1080p.WEB-DL.x264", SizeBytes: 4 * 1024 * 1024 * 1024},
		{Title: "The.Office.S04E01.2160p.WEB-DL.x265", SizeBytes: 8 * 1024 * 1024 * 1024},
	}, filter.Options{
		ExpectedTitle:  "The Office",
		ExpectedYear:   2005,
		EpisodeAirYear: 2009,
		IsMovie:        false,
		TargetSeason:   4,
		TargetEpisode:  1,
	})
	if len(results) != 3 {
		t.Fatalf("expected series-year, episode-air-year, and yearless results to pass, got %d", len(results))
	}

	applyEpisodeYearPriority(results, 2005, 2009)
	for _, result := range results {
		if result.Attributes["episodeYearPriority"] == "true" {
			t.Fatalf("valid episode air year activated conflict priority for %q", result.Title)
		}
		if strings.Contains(result.Title, ".2009.") && result.Attributes["episodeAirYearMatch"] != "true" {
			t.Fatalf("expected 2009 result to be tagged as a valid episode air year, got %+v", result.Attributes)
		}
	}
}

func TestScoreResult_BoundedScore(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingServicePriority, Name: "Service Priority", Enabled: true, Order: 0},
			{ID: config.RankingPreferredTerms, Name: "Preferred Terms", Enabled: true, Order: 1},
			{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 2},
			{ID: config.RankingLanguage, Name: "Language", Enabled: true, Order: 3},
			{ID: config.RankingSize, Name: "Size", Enabled: true, Order: 4},
			{ID: config.RankingPreferredScraper, Name: "Preferred Scraper", Enabled: true, Order: 5},
		},
		ServicePriority:    config.StreamingServicePriorityUsenet,
		PreferredTerms:     filter.CompileTerms([]string{"remux=10", "dv=10", "hdr=10"}),
		PreferredLang:      "eng",
		PreferredScraper:   "zilean",
		UseDownloadRanking: true,
		DownloadPreferredTerms: filter.CompileTerms([]string{
			"season pack=10",
			"complete=10",
		}),
	}

	result := models.NZBResult{
		Title:       "Show.S01.Complete.Season.Pack.2160p.REMUX.DV.HDR",
		SizeBytes:   120 * 1024 * 1024 * 1024,
		ServiceType: models.ServiceTypeUsenet,
		Indexer:     "zilean",
		Attributes:  map[string]string{"languages": "eng", "yearMatch": "true"},
	}

	score, _ := ScoreResult(result, ctx)
	if score > 1000000 {
		t.Fatalf("expected bounded score, got %d", score)
	}
}

func TestScoreResult_BreakdownHasReasons(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 0},
			{ID: config.RankingSize, Name: "Size", Enabled: true, Order: 1},
		},
	}

	r := models.NZBResult{Title: "Movie 2160p", SizeBytes: 5 * 1024 * 1024 * 1024}
	_, breakdown := ScoreResult(r, ctx)

	if len(breakdown) < 2 {
		t.Fatalf("expected at least 2 breakdown items, got %d", len(breakdown))
	}

	for _, b := range breakdown {
		if b.Criterion == "" {
			t.Fatal("breakdown item missing criterion name")
		}
		if b.Reason == "" {
			t.Fatal("breakdown item missing reason")
		}
	}
}

func TestScoreResult_PriorityOrderMatters(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingPreferredTerms, Name: "Preferred Terms", Enabled: true, Order: 0},
			{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 1},
		},
		PreferredTerms: filter.CompileTerms([]string{"remux"}),
	}

	// Remux 720p vs non-remux 2160p
	remux720 := models.NZBResult{Title: "Movie Remux 720p"}
	plain4k := models.NZBResult{Title: "Movie 2160p BluRay"}

	results := []models.NZBResult{plain4k, remux720}
	(&Service{}).sortResultsByScore(results, ctx)
	if results[0].Title != remux720.Title {
		t.Fatalf("expected preferred term match at position 0 to rank first, got %q", results[0].Title)
	}
}

func TestScoreResult_WeightedPreferredTerms(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingPreferredTerms, Name: "Preferred Terms", Enabled: true, Order: 0},
		},
		PreferredTerms: filter.CompileTerms([]string{"DV=3", "REMUX=2", "HDR"}),
	}

	// Matches DV(3) + REMUX(2) + HDR(1) = weight 6
	allMatch := models.NZBResult{Title: "Movie.2024.REMUX.DV.HDR"}
	// Matches only DV(3) = weight 3
	dvOnly := models.NZBResult{Title: "Movie.2024.DV.x265"}
	// No match = weight 0
	noMatch := models.NZBResult{Title: "Movie.2024.1080p.BluRay"}

	sAll, _ := ScoreResult(allMatch, ctx)
	sDV, _ := ScoreResult(dvOnly, ctx)
	sNone, _ := ScoreResult(noMatch, ctx)

	if sAll <= sDV {
		t.Fatalf("expected all-match (%d) > DV-only (%d)", sAll, sDV)
	}
	if sDV <= sNone {
		t.Fatalf("expected DV-only (%d) > no-match (%d)", sDV, sNone)
	}
}

func TestScoreResult_WeightedNonPreferredTerms(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingNonPreferredTerms, Name: "Non-Preferred Terms", Enabled: true, Order: 0},
		},
		NonPreferredTerms: filter.CompileTerms([]string{"CAM=3", "HDTS"}),
	}

	cam := models.NZBResult{Title: "Movie.2024.CAM"}      // weight 3
	hdts := models.NZBResult{Title: "Movie.2024.HDTS"}    // weight 1
	clean := models.NZBResult{Title: "Movie.2024.BluRay"} // weight 0

	sCam, _ := ScoreResult(cam, ctx)
	sHdts, _ := ScoreResult(hdts, ctx)
	sClean, _ := ScoreResult(clean, ctx)

	if sCam >= sHdts {
		t.Fatalf("expected CAM=3 (%d) < HDTS=1 (%d) — higher weight = more penalty", sCam, sHdts)
	}
	if sHdts >= sClean {
		t.Fatalf("expected HDTS (%d) < clean (%d)", sHdts, sClean)
	}
}

func TestScoreResult_MultipleWeightedTermsSum(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingPreferredTerms, Name: "Preferred Terms", Enabled: true, Order: 0},
		},
		PreferredTerms: filter.CompileTerms([]string{"DV=3", "HDR=2"}),
	}

	// Matches both DV(3) + HDR(2) = 5
	both := models.NZBResult{Title: "Movie.DV.HDR.2024"}
	// Matches only DV(3) = 3
	dvOnly := models.NZBResult{Title: "Movie.DV.2024"}

	sBoth, _ := ScoreResult(both, ctx)
	sDV, _ := ScoreResult(dvOnly, ctx)

	if sBoth <= sDV {
		t.Fatalf("expected both match (%d) > DV-only (%d)", sBoth, sDV)
	}
}

func TestScoreResult_BackwardCompat_NoWeights(t *testing.T) {
	// Terms without =N suffix should still work (weight defaults to 1)
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingPreferredTerms, Name: "Preferred Terms", Enabled: true, Order: 0},
		},
		PreferredTerms: filter.CompileTerms([]string{"remux"}),
	}

	with := models.NZBResult{Title: "Movie 2024 Remux 1080p"}
	without := models.NZBResult{Title: "Movie 2024 BluRay 1080p"}

	scoreWith, _ := ScoreResult(with, ctx)
	scoreWithout, _ := ScoreResult(without, ctx)

	if scoreWith <= scoreWithout {
		t.Fatalf("expected preferred term match (%d) > no match (%d)", scoreWith, scoreWithout)
	}
}

func TestScoreResult_DownloadPreferredTerms(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 0},
		},
		DownloadPreferredTerms: filter.CompileTerms([]string{"season pack=3", "complete"}),
		UseDownloadRanking:     true,
	}

	match := models.NZBResult{Title: "Show.S01.1080p.Season.Pack.Complete"}
	plain := models.NZBResult{Title: "Show.S01E01.2160p"}

	_, breakdownMatch := ScoreResult(match, ctx)
	results := []models.NZBResult{plain, match}
	(&Service{}).sortResultsByScore(results, ctx)
	if results[0].Title != match.Title {
		t.Fatalf("expected download preferred match to rank above plain result, got %q", results[0].Title)
	}
	found := false
	for _, item := range breakdownMatch {
		if item.Criterion == "Download Preferred Terms" {
			found = true
			if item.Points <= 0 {
				t.Fatalf("expected positive download preferred term score, got %d", item.Points)
			}
		}
	}
	if !found {
		t.Fatal("expected download preferred terms breakdown item")
	}
}

func TestScoreResult_DownloadPreferredTermsIgnoredWhenDisabled(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 0},
		},
		DownloadPreferredTerms: filter.CompileTerms([]string{"x265=3"}),
		UseDownloadRanking:     false,
	}

	match := models.NZBResult{Title: "Show.S01.1080p.WEBRip.x265"}
	plain := models.NZBResult{Title: "Show.S01E01.2160p.WEB-DL"}

	scoreMatch, breakdownMatch := ScoreResult(match, ctx)
	scorePlain, _ := ScoreResult(plain, ctx)

	if scoreMatch >= scorePlain {
		t.Fatalf("expected disabled download terms not to outrank 2160p result (%d >= %d)", scoreMatch, scorePlain)
	}
	for _, item := range breakdownMatch {
		if item.Criterion == "Download Preferred Terms" {
			t.Fatal("did not expect download preferred terms breakdown when download ranking is disabled")
		}
	}
}

func TestSortResultsByScore_NegativeScoresLast(t *testing.T) {
	ctx := ScoringContext{
		RankingCriteria: []config.RankingCriterion{
			{ID: config.RankingNonPreferredTerms, Name: "Non-Preferred Terms", Enabled: true, Order: 0},
			{ID: config.RankingResolution, Name: "Resolution", Enabled: true, Order: 1},
		},
		NonPreferredTerms: filter.CompileTerms([]string{"french=2"}),
	}

	results := []models.NZBResult{
		{Title: "Wayward.Pines.S01E03.FRENCH.1080p.WEB-DL"},
		{Title: "Wayward.Pines.S01E03.DVDRip"},
		{Title: "Wayward.Pines.S01E03.1080p.WEB-DL"},
	}

	(&Service{}).sortResultsByScore(results, ctx)

	if results[0].Title != "Wayward.Pines.S01E03.1080p.WEB-DL" {
		t.Fatalf("expected positive-scored result first, got %q", results[0].Title)
	}
	if results[1].Title != "Wayward.Pines.S01E03.DVDRip" {
		t.Fatalf("expected neutral-scored result second, got %q", results[1].Title)
	}
	if results[2].Title != "Wayward.Pines.S01E03.FRENCH.1080p.WEB-DL" {
		t.Fatalf("expected negative-scored result last, got %q", results[2].Title)
	}
}
