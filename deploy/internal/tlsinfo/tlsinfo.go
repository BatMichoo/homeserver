// Package tlsinfo reads a TLS server's certificate, for its expiry.
package tlsinfo

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"time"
)

// Expiry is when the certificate served at host:port expires. It only
// reads the certificate, so it doesn't verify it (the home CA isn't in
// the deployer's trust store).
func Expiry(ctx context.Context, host, port string) (time.Time, error) {
	d := tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 10 * time.Second},
		Config:    &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // reading only
	}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return time.Time{}, err
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return time.Time{}, errors.New("no certificate")
	}
	return certs[0].NotAfter, nil
}
