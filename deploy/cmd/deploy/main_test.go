package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLintCommand(t *testing.T) {
	var out bytes.Buffer
	bad := `{"name":"x","services":{"web":{"image":"busybox"}}}`
	assert.Equal(t, 1, lint([]string{"--project-dir", "/x"}, strings.NewReader(bad), &out))
	assert.Contains(t, out.String(), "service web")
	out.Reset()
	good := `{"name":"x","services":{"web":{"image":"busybox","healthcheck":{"test":["CMD","true"]}}}}`
	assert.Equal(t, 0, lint(nil, strings.NewReader(good), &out))
	assert.Equal(t, 2, lint(nil, strings.NewReader("nope"), &out))
}
