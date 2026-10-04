package filter

// RealDebridRestrictedReleaseTerm reflects the filename rules retested on
// 2026-10-03: https://debridmediamanager.com/rd-filename-filters.html.
// RD matches these five substrings exactly, including case and dot separators.
// Disable CompileTerms' default case-insensitive matching explicitly.
const RealDebridRestrictedReleaseTerm = `/(?-i)(?:WEB-DL|WEB\.x264|WEB\.H264|HDTV\.x264|HDTV\.XviD)/`
