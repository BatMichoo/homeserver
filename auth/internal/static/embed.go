// Package static holds the auth service's own stylesheet.
package static

import "embed"

//go:embed *.css
var FS embed.FS
