// Package keydockconnect exposes the release version shared with the installer.
package keydockconnect

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var releaseVersion string

// Version returns the semantic version without a leading "v".
func Version() string { return strings.TrimSpace(releaseVersion) }
