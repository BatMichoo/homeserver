// Package static holds the auth service's own stylesheet and icon
// (favicon.png, drawn from favicon.svg).
package static

import "embed"

//go:embed *.css favicon.png
var FS embed.FS
