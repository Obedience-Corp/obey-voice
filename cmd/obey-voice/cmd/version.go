package cmd

// releaseVersion is set by the native release recipes with -ldflags -X.
// An empty value preserves Fang's development-build version detection.
var releaseVersion string
