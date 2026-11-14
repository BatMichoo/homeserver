package accounts

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSafeNext(t *testing.T) {
	const d = "homeserver.lan"
	for next, want := range map[string]string{
		"https://expenser.homeserver.lan:8443/a?b=c": "https://expenser.homeserver.lan:8443/a?b=c",
		"https://homeserver.lan/":                    "https://homeserver.lan/",
		"https://auth.homeserver.lan/account":        "https://auth.homeserver.lan/account",
		"/account?x=1":                               "/account?x=1",
		"https://evil.com/":                          "/account",
		"//evil.com":                                 "/account",
		`/\evil.com`:                                 "/account",
		"https://homeserver.lan.evil.com/":           "/account",
		"https://evilhomeserver.lan/":                "/account",
		"http://homeserver.lan/":                     "/account",
		"javascript:alert(1)":                        "/account",
		"https://user@homeserver.lan/":               "/account",
		"":                                           "/account",
		"%zz":                                        "/account",
		"account":                                    "/account",
	} {
		assert.Equal(t, want, SafeNext(next, d), "next %q", next)
	}
}
