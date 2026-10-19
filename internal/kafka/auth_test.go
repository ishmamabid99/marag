package kafka

import "testing"

func TestClientAuthOpts(t *testing.T) {
	tests := []struct {
		name    string
		auth    ClientAuth
		wantErr bool
	}{
		{"empty is plaintext, no error", ClientAuth{}, false},
		{"sasl plain", ClientAuth{SASLUser: "u", SASLPass: "p"}, false},
		{"sasl over tls", ClientAuth{SASLUser: "u", SASLPass: "p", TLSEnabled: true}, false},
		{"compression snappy", ClientAuth{Compression: "snappy"}, false},
		{"compression none", ClientAuth{Compression: "none"}, false},
		{"compression zstd", ClientAuth{Compression: "zstd"}, false},
		{"bad compression errors", ClientAuth{Compression: "bogus"}, true},
		{"retries wired", ClientAuth{MaxRetries: 5}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := tt.auth.Opts()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Opts: %v", err)
			}
			if opts == nil {
				t.Fatalf("want non-nil opts slice")
			}
		})
	}
}
