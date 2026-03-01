package tls

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"

	pcrypto "github.com/ShigShag/Phantom-Proxy/internal/crypto"
	"github.com/ShigShag/Phantom-Proxy/internal/transport"
)

func init() {
	transport.Register(&TLS{})
}

// TLS implements the Transport interface for TLS connections.
type TLS struct{}

func (t *TLS) Name() string { return "tls" }

func (t *TLS) Dial(addr string, cfg *transport.Config) (net.Conn, error) {
	tlsCfg := &tls.Config{
		// Default to skipping verification since HMAC auth handles trust.
		// Override with --tls-ca to pin a specific server cert.
		InsecureSkipVerify: true,
	}

	// If a CA was explicitly provided, use it for verification.
	if cfg.CAFile != "" {
		tlsCfg.InsecureSkipVerify = cfg.SkipVerify
		caCert, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA cert: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA cert")
		}
		tlsCfg.RootCAs = pool
	}

	// Load client certificate for mTLS (optional).
	if cfg.CertFile != "" && cfg.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load client cert: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}

	return tls.Dial("tcp", addr, tlsCfg)
}

func (t *TLS) Listen(addr string, cfg *transport.Config) (net.Listener, error) {
	var cert tls.Certificate

	if cfg.CertFile != "" && cfg.KeyFile != "" {
		// Load from files.
		var err error
		cert, err = tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load server cert: %w", err)
		}
	} else {
		// Auto-generate an ephemeral self-signed certificate.
		certPEM, keyPEM, err := pcrypto.GenerateSelfSignedCert()
		if err != nil {
			return nil, fmt.Errorf("generate cert: %w", err)
		}
		cert, err = tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return nil, fmt.Errorf("parse generated cert: %w", err)
		}
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
	}

	// Optional mTLS: require and verify client certificates.
	if cfg.CAFile != "" {
		caCert, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA cert: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA cert")
		}
		tlsCfg.ClientCAs = pool
		tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return tls.Listen("tcp", addr, tlsCfg)
}
