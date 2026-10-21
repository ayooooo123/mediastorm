package calendar

// UserCalendarView is a built calendar snapshot as served to handlers.
type UserCalendarView = userCalendar

// GetForHome returns the best calendar available for the Home calendar rows
// without ever blocking on a build: the full calendar, else the lite one, else
// the last calendar from before an invalidation. Whenever the full calendar is
// missing a background build is started (single-flighted). pending is true
// when nothing at all could be served yet.
func (s *Service) GetForHome(userID string) (uc *userCalendar, pending bool) {
	if full := s.peek(userID); full != nil {
		return full, false
	}
	s.kickHomeBuilds(userID)
	if lite := s.peekLite(userID); lite != nil {
		return lite, false
	}
	s.mu.RLock()
	stale := s.stale[userID]
	s.mu.RUnlock()
	if stale != nil {
		return stale, false
	}
	return nil, true
}

// kickHomeBuilds starts the lite (fast, fills Home quickly) and full builds in
// the background. Both are single-flighted per user and return immediately
// when a cached entry already exists.
func (s *Service) kickHomeBuilds(userID string) {
	go s.buildAndCacheLiteUserCalendar(userID, false, nil)
	go s.buildAndCacheUserCalendar(userID, false)
}
