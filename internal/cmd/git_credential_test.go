package cmd

import (
	"os"
	"testing"
)

func TestReadCredentialRequest(t *testing.T) {
	content := "protocol=https\nhost=github.com\npath=foo/bar.git\n\n"
	file, err := createTempReaderFile(t, content)
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	req, err := readCredentialRequest(file)
	if err != nil {
		t.Fatalf("readCredentialRequest failed: %v", err)
	}
	if req.protocol != "https" || req.host != "github.com" || req.path != "foo/bar.git" {
		t.Fatalf("unexpected request: %+v", req)
	}
}

func createTempReaderFile(t *testing.T, content string) (*os.File, error) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "cred-*")
	if err != nil {
		return nil, err
	}
	if _, err := f.WriteString(content); err != nil {
		return nil, err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	return f, nil
}
