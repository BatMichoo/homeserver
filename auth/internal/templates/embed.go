// Package templates holds the auth service's page templates, built into
// the binary. They're content for the theme's page shell (theme/page).
package templates

import "embed"

//go:embed pages/*.html
var FS embed.FS
