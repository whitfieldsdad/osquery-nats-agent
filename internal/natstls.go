package internal

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
)

// NATSTLSConfig builds the TLS configuration for the NATS connection from cfg's TLS
// settings, or returns nil if none are set (the connection then uses TLS only if the
// server or a tls:// URL requires it, verified against the system's trusted roots).
//
// With a client certificate and key the agent authenticates itself to the server (mutual
// TLS); with a CA file the server's certificate is verified against those CAs instead of
// the system's. Either implies TLS: connecting to a server without TLS fails. The files
// are read here, so a missing or invalid file is reported before connecting.
func NATSTLSConfig(cfg Config) (*tls.Config, error) {
	if cfg.NATSTLSCertPath == "" && cfg.NATSTLSKeyPath == "" && cfg.NATSTLSCAPath == "" {
		return nil, nil
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}

	if cfg.NATSTLSCertPath != "" || cfg.NATSTLSKeyPath != "" {
		if cfg.NATSTLSCertPath == "" || cfg.NATSTLSKeyPath == "" {
			return nil, errors.New("NATS TLS: a client certificate and key must be set together")
		}
		cert, err := tls.LoadX509KeyPair(cfg.NATSTLSCertPath, cfg.NATSTLSKeyPath)
		if err != nil {
			return nil, fmt.Errorf("NATS TLS: load client certificate %s and key %s: %w",
				cfg.NATSTLSCertPath, cfg.NATSTLSKeyPath, err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	if cfg.NATSTLSCAPath != "" {
		pem, err := os.ReadFile(cfg.NATSTLSCAPath)
		if err != nil {
			return nil, fmt.Errorf("NATS TLS: read CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("NATS TLS: no PEM certificates found in CA file %s", cfg.NATSTLSCAPath)
		}
		tlsConfig.RootCAs = pool
	}
	return tlsConfig, nil
}
