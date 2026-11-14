package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/BatMichoo/homeserver/auth/internal/accounts"
	"github.com/BatMichoo/homeserver/auth/internal/db/dbtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResetPasswordCommand(t *testing.T) {
	acc := accounts.New(dbtest.New(t), time.Hour)
	ctx := context.Background()
	_, err := acc.Register(ctx, "ivan", "secret1", "secret1")
	require.NoError(t, err)

	var out bytes.Buffer
	assert.Equal(t, 0, command(ctx, []string{"reset-password", "ivan"}, &out, acc))
	line := strings.TrimSpace(out.String())
	require.True(t, strings.HasPrefix(line, "New password for ivan: "), line)
	_, err = acc.Login(ctx, "ivan", strings.TrimPrefix(line, "New password for ivan: "))
	assert.NoError(t, err)

	out.Reset()
	assert.Equal(t, 1, command(ctx, []string{"reset-password", "nobody"}, &out, acc))
	assert.Contains(t, out.String(), "nobody")
	out.Reset()
	assert.Equal(t, 2, command(ctx, []string{"reset-password"}, &out, acc))
	assert.Equal(t, 2, command(ctx, []string{"frobnicate"}, &out, acc))
}
