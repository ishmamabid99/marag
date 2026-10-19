package kafka

import (
	"crypto/tls"
	"fmt"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/plain"
)

// ClientAuth carries the shared broker auth + client tuning applied to every
// producer and consumer. The zero value is plaintext, no compression, and
// franz-go's default retries — matching the pre-SASL behavior.
type ClientAuth struct {
	SASLUser    string // empty ⇒ no SASL (plaintext)
	SASLPass    string
	TLSEnabled  bool   // true ⇒ dial over TLS (SASL_SSL when combined with SASL)
	Compression string // "", "none", "gzip", "snappy", "lz4", "zstd"
	MaxRetries  int    // <=0 ⇒ franz-go default
}

// Opts translates the auth/tuning config into franz-go client options.
func (a ClientAuth) Opts() ([]kgo.Opt, error) {
	opts := []kgo.Opt{}
	if a.SASLUser != "" {
		opts = append(opts, kgo.SASL(plain.Auth{User: a.SASLUser, Pass: a.SASLPass}.AsMechanism()))
	}
	if a.TLSEnabled {
		opts = append(opts, kgo.DialTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12}))
	}
	if a.Compression != "" {
		codec, err := compressionCodec(a.Compression)
		if err != nil {
			return nil, err
		}
		opts = append(opts, kgo.ProducerBatchCompression(codec))
	}
	if a.MaxRetries > 0 {
		opts = append(opts, kgo.RecordRetries(a.MaxRetries))
	}
	return opts, nil
}

func compressionCodec(name string) (kgo.CompressionCodec, error) {
	switch name {
	case "none":
		return kgo.NoCompression(), nil
	case "gzip":
		return kgo.GzipCompression(), nil
	case "snappy":
		return kgo.SnappyCompression(), nil
	case "lz4":
		return kgo.Lz4Compression(), nil
	case "zstd":
		return kgo.ZstdCompression(), nil
	default:
		return kgo.CompressionCodec{}, fmt.Errorf("kafka: unknown compression %q", name)
	}
}
