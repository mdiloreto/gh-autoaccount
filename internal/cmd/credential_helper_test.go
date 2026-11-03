package cmd

import (
	"strings"
	"testing"
)

func TestParseCredentialInput(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantProtocol string
		wantHost     string
		wantPath     string
	}{
		{
			name:         "standard github https request",
			input:        "protocol=https\nhost=github.com\n\n",
			wantProtocol: "https",
			wantHost:     "github.com",
		},
		{
			name:         "with path",
			input:        "protocol=https\nhost=github.com\npath=example/project.git\n\n",
			wantProtocol: "https",
			wantHost:     "github.com",
			wantPath:     "example/project.git",
		},
		{
			name:         "ssh protocol",
			input:        "protocol=ssh\nhost=github.com\n\n",
			wantProtocol: "ssh",
			wantHost:     "github.com",
		},
		{
			name:         "different host",
			input:        "protocol=https\nhost=git.example.invalid\n\n",
			wantProtocol: "https",
			wantHost:     "git.example.invalid",
		},
		{
			name:         "empty input",
			input:        "\n",
			wantProtocol: "",
			wantHost:     "",
		},
		{
			name:         "no trailing blank line (EOF terminated)",
			input:        "protocol=https\nhost=github.com",
			wantProtocol: "https",
			wantHost:     "github.com",
		},
		{
			name:         "unknown keys are ignored",
			input:        "protocol=https\nhost=github.com\nwwwauth=Basic\n\n",
			wantProtocol: "https",
			wantHost:     "github.com",
		},
		{
			name:         "malformed lines are skipped",
			input:        "protocol=https\nbadline\nhost=github.com\n\n",
			wantProtocol: "https",
			wantHost:     "github.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := strings.NewReader(tt.input)
			fields := parseCredentialInput(r)
			if fields.Protocol != tt.wantProtocol {
				t.Errorf("Protocol = %q, want %q", fields.Protocol, tt.wantProtocol)
			}
			if fields.Host != tt.wantHost {
				t.Errorf("Host = %q, want %q", fields.Host, tt.wantHost)
			}
			if fields.Path != tt.wantPath {
				t.Errorf("Path = %q, want %q", fields.Path, tt.wantPath)
			}
		})
	}
}
