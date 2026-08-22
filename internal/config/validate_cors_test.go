package config

import "testing"

func TestValidateCORSOrigins(t *testing.T) {
	tests := []struct {
		name    string
		origins []string
		wantErr bool
	}{
		{name: "empty development wildcard"},
		{name: "https", origins: []string{"https://accounts.example.com"}},
		{name: "loopback http", origins: []string{"http://127.0.0.1:5180", "http://localhost:5180"}},
		{name: "wildcard", origins: []string{"*"}, wantErr: true},
		{name: "non-loopback http", origins: []string{"http://accounts.example.com"}, wantErr: true},
		{name: "path", origins: []string{"https://accounts.example.com/app"}, wantErr: true},
		{name: "trailing slash", origins: []string{"https://accounts.example.com/"}, wantErr: true},
		{name: "duplicate", origins: []string{"https://accounts.example.com", "https://accounts.example.com"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateCORSOrigins(test.origins)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateCORSOrigins() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}
