package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTestCertPair generates a throwaway self-signed cert/key pair on disk.
func writeTestCertPair(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certOut, err := os.Create(certPath)
	if err != nil {
		t.Fatalf("create cert file: %v", err)
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatalf("encode cert: %v", err)
	}
	certOut.Close()

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyOut, err := os.Create(keyPath)
	if err != nil {
		t.Fatalf("create key file: %v", err)
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		t.Fatalf("encode key: %v", err)
	}
	keyOut.Close()
	return certPath, keyPath
}

func TestEffectiveTLSMode(t *testing.T) {
	certPath, keyPath := writeTestCertPair(t)

	tests := []struct {
		name string
		cfg  Config
		want TLSMode
	}{
		{name: "disabled", cfg: Config{}, want: TLSModeOff},
		{name: "enabled but empty", cfg: Config{TLSEnabled: true}, want: TLSModeOff},
		{name: "cert files", cfg: Config{TLSEnabled: true, TLSCertFile: certPath, TLSKeyFile: keyPath}, want: TLSModeFiles},
		{name: "auto domain", cfg: Config{TLSEnabled: true, TLSAutoDomain: "seedstream.example.com"}, want: TLSModeAuto},
		{
			name: "auto domain wins over cert files",
			cfg:  Config{TLSEnabled: true, TLSAutoDomain: "seedstream.example.com", TLSCertFile: certPath, TLSKeyFile: keyPath},
			want: TLSModeAuto,
		},
		{
			name: "cert without key is unusable",
			cfg:  Config{TLSEnabled: true, TLSCertFile: certPath},
			want: TLSModeOff,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.EffectiveTLSMode(); got != tt.want {
				t.Fatalf("EffectiveTLSMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateTLS(t *testing.T) {
	certPath, keyPath := writeTestCertPair(t)
	otherCert, _ := writeTestCertPair(t)

	t.Run("off is always valid", func(t *testing.T) {
		cfg := &Config{}
		if err := cfg.ValidateTLS(); err != nil {
			t.Fatalf("ValidateTLS() = %v, want nil", err)
		}
	})

	t.Run("valid pair", func(t *testing.T) {
		cfg := &Config{TLSEnabled: true, TLSCertFile: certPath, TLSKeyFile: keyPath}
		if err := cfg.ValidateTLS(); err != nil {
			t.Fatalf("ValidateTLS() = %v, want nil", err)
		}
	})

	t.Run("enabled with nothing configured", func(t *testing.T) {
		cfg := &Config{TLSEnabled: true}
		if err := cfg.ValidateTLS(); err == nil {
			t.Fatalf("expected an error when HTTPS is on with no certificate")
		}
	})

	t.Run("missing cert file", func(t *testing.T) {
		cfg := &Config{TLSEnabled: true, TLSCertFile: filepath.Join(t.TempDir(), "nope.pem"), TLSKeyFile: keyPath}
		if err := cfg.ValidateTLS(); err == nil {
			t.Fatalf("expected an error for an unreadable certificate")
		}
	})

	t.Run("mismatched cert and key", func(t *testing.T) {
		cfg := &Config{TLSEnabled: true, TLSCertFile: otherCert, TLSKeyFile: keyPath}
		if err := cfg.ValidateTLS(); err == nil {
			t.Fatalf("expected an error when the key does not match the certificate")
		}
	})

	t.Run("auto domain must be a hostname", func(t *testing.T) {
		for _, bad := range []string{"https://seedstream.example.com", "seedstream.example.com:7000", "localhost"} {
			cfg := &Config{TLSEnabled: true, TLSAutoDomain: bad}
			if err := cfg.ValidateTLS(); err == nil {
				t.Fatalf("expected an error for auto domain %q", bad)
			}
		}
		cfg := &Config{TLSEnabled: true, TLSAutoDomain: "seedstream.example.com"}
		if err := cfg.ValidateTLS(); err != nil {
			t.Fatalf("ValidateTLS() = %v, want nil for a valid domain", err)
		}
	})
}

func TestBaseURLSchemeMismatch(t *testing.T) {
	certPath, keyPath := writeTestCertPair(t)

	t.Run("http base url while serving TLS is flagged", func(t *testing.T) {
		cfg := &Config{
			AddonBaseURL: "http://seedstream.example.com:7000",
			TLSEnabled:   true, TLSCertFile: certPath, TLSKeyFile: keyPath,
		}
		mismatch, want := cfg.BaseURLSchemeMismatch()
		if !mismatch || want != "https" {
			t.Fatalf("BaseURLSchemeMismatch() = (%v, %q), want (true, \"https\")", mismatch, want)
		}
	})

	t.Run("matching https base url is fine", func(t *testing.T) {
		cfg := &Config{
			AddonBaseURL: "https://seedstream.example.com",
			TLSEnabled:   true, TLSCertFile: certPath, TLSKeyFile: keyPath,
		}
		if mismatch, _ := cfg.BaseURLSchemeMismatch(); mismatch {
			t.Fatalf("did not expect a mismatch for an https base URL with TLS on")
		}
	})

	t.Run("https base url with TLS off is the reverse proxy case", func(t *testing.T) {
		cfg := &Config{AddonBaseURL: "https://seedstream.example.com"}
		if mismatch, _ := cfg.BaseURLSchemeMismatch(); mismatch {
			t.Fatalf("an https base URL behind a reverse proxy must not be flagged")
		}
	})

	t.Run("empty base url is not flagged", func(t *testing.T) {
		cfg := &Config{TLSEnabled: true, TLSCertFile: certPath, TLSKeyFile: keyPath}
		if mismatch, _ := cfg.BaseURLSchemeMismatch(); mismatch {
			t.Fatalf("an empty base URL must not be flagged")
		}
	})
}
