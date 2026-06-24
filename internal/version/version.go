package version

// GitTag is set at build time via -ldflags for tagged releases.
var GitTag string

// BuildTime is set at build time via -ldflags to the ISO8601 UTC timestamp
// of the CI build. Used as the displayed version when no GitTag is present.
var BuildTime string
