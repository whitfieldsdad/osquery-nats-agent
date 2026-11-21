package internal

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testCert is a certificate and key, both in memory and written to PEM files.
type testCert struct {
	cert              *x509.Certificate
	key               *ecdsa.PrivateKey
	certPath, keyPath string
}

// newTestCert creates a certificate signed by parent (self-signed if parent is nil) and
// writes it and its key to dir as <name>.pem and <name>-key.pem.
func newTestCert(t *testing.T, dir, name string, parent *testCert, usage x509.ExtKeyUsage) *testCert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	signer, signerKey := tmpl, key
	if parent == nil {
		tmpl.IsCA = true
		tmpl.BasicConstraintsValid = true
		tmpl.KeyUsage = x509.KeyUsageCertSign
	} else {
		signer, signerKey = parent.cert, parent.key
		tmpl.KeyUsage = x509.KeyUsageDigitalSignature
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{usage}
		tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	tc := &testCert{
		cert:     cert,
		key:      key,
		certPath: filepath.Join(dir, name+".pem"),
		keyPath:  filepath.Join(dir, name+"-key.pem"),
	}
	writePEM(t, tc.certPath, "CERTIFICATE", der)
	writePEM(t, tc.keyPath, "PRIVATE KEY", keyDER)
	return tc
}

func writePEM(t *testing.T, path, blockType string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}

// handshake connects to a TLS server that requires a client certificate signed by ca,
// using client as the client's TLS config, and returns the handshake error.
func handshake(t *testing.T, ca, server *testCert, client *tls.Config) error {
	t.Helper()
	clientCAs := x509.NewCertPool()
	clientCAs.AddCert(ca.cert)
	serverCert, err := tls.LoadX509KeyPair(server.certPath, server.keyPath)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAs,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.(*tls.Conn).Handshake()
	}()

	client = client.Clone()
	client.ServerName = "127.0.0.1" // nats.go sets this from the server URL
	conn, err := tls.Dial("tcp", ln.Addr().String(), client)
	if err != nil {
		return err
	}
	defer conn.Close()
	// In TLS 1.3 the server checks the client certificate after the client's handshake
	// completes; a rejection only shows up on the next read.
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err != nil && !strings.Contains(err.Error(), "EOF") {
		return err
	}
	return nil
}

func TestNATSTLSConfigMutualTLS(t *testing.T) {
	dir := t.TempDir()
	ca := newTestCert(t, dir, "ca", nil, 0)
	server := newTestCert(t, dir, "server", ca, x509.ExtKeyUsageServerAuth)
	client := newTestCert(t, dir, "client", ca, x509.ExtKeyUsageClientAuth)
	otherCA := newTestCert(t, dir, "other-ca", nil, 0)

	t.Run("client certificate accepted", func(t *testing.T) {
		tlsConfig, err := NATSTLSConfig(Config{
			NATSTLSCertPath: client.certPath,
			NATSTLSKeyPath:  client.keyPath,
			NATSTLSCAPath:   ca.certPath,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := handshake(t, ca, server, tlsConfig); err != nil {
			t.Fatalf("mutual TLS handshake failed: %v", err)
		}
	})

	t.Run("no client certificate rejected", func(t *testing.T) {
		tlsConfig, err := NATSTLSConfig(Config{NATSTLSCAPath: ca.certPath})
		if err != nil {
			t.Fatal(err)
		}
		if err := handshake(t, ca, server, tlsConfig); err == nil {
			t.Fatal("handshake without a client certificate succeeded, want it rejected")
		}
	})

	t.Run("untrusted server rejected", func(t *testing.T) {
		tlsConfig, err := NATSTLSConfig(Config{
			NATSTLSCertPath: client.certPath,
			NATSTLSKeyPath:  client.keyPath,
			NATSTLSCAPath:   otherCA.certPath,
		})
		if err != nil {
			t.Fatal(err)
		}
		err = handshake(t, ca, server, tlsConfig)
		if err == nil || !strings.Contains(err.Error(), "certificate signed by unknown authority") {
			t.Fatalf("err = %v, want the server certificate to be rejected", err)
		}
	})
}

func TestNATSTLSConfigNotSet(t *testing.T) {
	tlsConfig, err := NATSTLSConfig(Config{})
	if err != nil || tlsConfig != nil {
		t.Fatalf("NATSTLSConfig() = %v, %v; want nil, nil", tlsConfig, err)
	}
}

func TestNATSTLSConfigErrors(t *testing.T) {
	dir := t.TempDir()
	ca := newTestCert(t, dir, "ca", nil, 0)
	client := newTestCert(t, dir, "client", ca, x509.ExtKeyUsageClientAuth)
	other := newTestCert(t, dir, "other", ca, x509.ExtKeyUsageClientAuth)
	notPEM := filepath.Join(dir, "not-pem.txt")
	if err := os.WriteFile(notPEM, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		cfg     Config
		wantErr string
	}{
		"cert without key":  {Config{NATSTLSCertPath: client.certPath}, "must be set together"},
		"missing cert file": {Config{NATSTLSCertPath: filepath.Join(dir, "nope.pem"), NATSTLSKeyPath: client.keyPath}, "load client certificate"},
		"mismatched key":    {Config{NATSTLSCertPath: client.certPath, NATSTLSKeyPath: other.keyPath}, "private key does not match"},
		"missing CA file":   {Config{NATSTLSCAPath: filepath.Join(dir, "nope.pem")}, "read CA file"},
		"CA file not PEM":   {Config{NATSTLSCAPath: notPEM}, "no PEM certificates"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NATSTLSConfig(tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}
