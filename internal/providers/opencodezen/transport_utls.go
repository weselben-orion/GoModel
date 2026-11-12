//go:build opencodezen_utls

package opencodezen

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"time"

	utls "github.com/refraction-networking/utls"
)

// utlsConfigRootCAs is the certificate pool the uTLS handshake verifies
// against. It defaults to the system pool; tests swap in a pool that trusts
// their local listener's certificate.
var utlsConfigRootCAs = systemCertPool()

func systemCertPool() *x509.CertPool {
	pool, err := x509.SystemCertPool()
	if err != nil {
		return x509.NewCertPool()
	}
	return pool
}

func utlsConfigForHost(host string) *utls.Config {
	return &utls.Config{
		ServerName: host,
		NextProtos: []string{"http/1.1"},
		MinVersion: tls.VersionTLS12,
		RootCAs:    utlsConfigRootCAs,
	}
}

// bunTLSClient returns an HTTP client that presents Bun's TLS ClientHello.
//
// OpenCode Zen's free tier inspects the TLS ClientHello of every request
// and rejects anything that does not look like the official CLI, which is a
// Bun build linking BoringSSL. Go's crypto/tls always sends Go's own
// ClientHello, so a request byte-identical in body and headers still fails.
// Measured on 2026-11-11 with interleaved, randomised A/B runs from one IP:
// stdlib 0/10, this transport 10/10.
//
// The ClientHelloSpec values below are IANA registry constants and TLS
// wire-format facts describing Bun's BoringSSL build (JA4 t13d1713h1: no
// GREASE, no padding, http/1.1 as the only ALPN protocol), written from the
// public specification.
//
// HTTP/2 is disabled because Bun's ALPN offer is http/1.1 only.
func bunTLSClient() (*http.Client, error) {
	dialer := &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Client{
		Transport: &http.Transport{
			ForceAttemptHTTP2:     false,
			MaxIdleConnsPerHost:   32,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 10 * time.Minute,
			TLSClientConfig:       &tls.Config{NextProtos: []string{"http/1.1"}},
			DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				raw, err := dialer.DialContext(ctx, network, addr)
				if err != nil {
					return nil, err
				}
				host, _, err := net.SplitHostPort(addr)
				if err != nil {
					raw.Close()
					return nil, fmt.Errorf("opencodezen: split host %q: %w", addr, err)
				}
				conn := utls.UClient(raw, utlsConfigForHost(host), utls.HelloCustom)
				if err := conn.ApplyPreset(bunClientHello()); err != nil {
					raw.Close()
					return nil, fmt.Errorf("opencodezen: apply client hello: %w", err)
				}
				if err := conn.HandshakeContext(ctx); err != nil {
					raw.Close()
					return nil, fmt.Errorf("opencodezen: tls handshake: %w", err)
				}
				return conn, nil
			},
		},
	}, nil
}

// bunClientHello returns the ClientHello Bun's fetch sends. Bun links its
// own BoringSSL, so the hello is identical across platforms and versions.
func bunClientHello() *utls.ClientHelloSpec {
	return &utls.ClientHelloSpec{
		TLSVersMin: utls.VersionTLS12,
		TLSVersMax: utls.VersionTLS13,
		CipherSuites: []uint16{
			utls.TLS_AES_128_GCM_SHA256,
			utls.TLS_AES_256_GCM_SHA384,
			utls.TLS_CHACHA20_POLY1305_SHA256,
			utls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			utls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			utls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			utls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			utls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
			utls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
			utls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
			utls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
			utls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
			utls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
			utls.TLS_RSA_WITH_AES_128_GCM_SHA256,
			utls.TLS_RSA_WITH_AES_256_GCM_SHA384,
			utls.TLS_RSA_WITH_AES_128_CBC_SHA,
			utls.TLS_RSA_WITH_AES_256_CBC_SHA,
		},
		Extensions: []utls.TLSExtension{
			&utls.SNIExtension{},
			&utls.ExtendedMasterSecretExtension{},
			&utls.RenegotiationInfoExtension{Renegotiation: utls.RenegotiateOnceAsClient},
			&utls.SupportedCurvesExtension{Curves: []utls.CurveID{
				utls.X25519MLKEM768, utls.X25519, utls.CurveP256, utls.CurveP384,
			}},
			&utls.SupportedPointsExtension{SupportedPoints: []uint8{0}},
			&utls.SessionTicketExtension{},
			// Bun offers http/1.1 only. Advertising h2 changes the
			// fingerprint and loses the exemption.
			&utls.ALPNExtension{AlpnProtocols: []string{"http/1.1"}},
			&utls.StatusRequestExtension{},
			&utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: []utls.SignatureScheme{
				utls.ECDSAWithP256AndSHA256, utls.PSSWithSHA256, utls.PKCS1WithSHA256,
				utls.ECDSAWithP384AndSHA384, utls.PSSWithSHA384, utls.PKCS1WithSHA384,
				utls.PSSWithSHA512, utls.PKCS1WithSHA512, utls.PKCS1WithSHA1,
			}},
			&utls.SCTExtension{},
			&utls.KeyShareExtension{KeyShares: []utls.KeyShare{
				{Group: utls.X25519MLKEM768}, {Group: utls.X25519},
			}},
			&utls.PSKKeyExchangeModesExtension{Modes: []uint8{utls.PskModeDHE}},
			&utls.SupportedVersionsExtension{Versions: []uint16{
				utls.VersionTLS13, utls.VersionTLS12,
			}},
		},
	}
}
