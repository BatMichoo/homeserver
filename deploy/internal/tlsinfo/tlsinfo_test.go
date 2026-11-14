package tlsinfo

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpiry(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	host, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)
	got, err := Expiry(context.Background(), host, port)
	require.NoError(t, err)
	assert.True(t, got.Equal(srv.Certificate().NotAfter))

	_, err = Expiry(context.Background(), "127.0.0.1", "1")
	assert.Error(t, err)
}
