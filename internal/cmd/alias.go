package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

func parseAliasMapping(data []byte) (map[string]string, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if err == io.EOF {
			return map[string]string{}, nil
		}
		return nil, errors.New("cannot parse alias list")
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("invalid alias list documents")
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode || doc.Content[0].Tag != "!!map" {
		return nil, errors.New("invalid alias mapping")
	}
	result := make(map[string]string)
	content := doc.Content[0].Content
	for i := 0; i < len(content); i += 2 {
		key, value := content[i], content[i+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
			return nil, errors.New("invalid alias entry")
		}
		if _, exists := result[key.Value]; exists {
			return nil, errors.New("duplicate alias")
		}
		result[key.Value] = value.Value
	}
	return result, nil
}

type aliasOutput struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (w *aliasOutput) Write(p []byte) (int, error) {
	if len(p) > 64*1024-w.Len() {
		w.cancel()
		return 0, errors.New("alias output too large")
	}
	return w.Buffer.Write(p)
}

func runAliasGH(ctx context.Context, realGH string, args ...string) ([]byte, error) {
	if !filepath.IsAbs(realGH) {
		return nil, errors.New("gh path is not absolute")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out := &aliasOutput{cancel: cancel}
	command := exec.CommandContext(ctx, realGH, args...)
	command.Stdout = out
	command.WaitDelay = time.Second
	if err := command.Run(); err != nil {
		return nil, errors.New("gh alias operation failed")
	}
	return out.Bytes(), nil
}

func readShortAlias(ctx context.Context, realGH string) (string, bool, error) {
	data, err := runAliasGH(ctx, realGH, "alias", "list")
	if err != nil {
		return "", false, err
	}
	aliases, err := parseAliasMapping(data)
	if err != nil {
		return "", false, err
	}
	value, exists := aliases["aa"]
	return value, exists, nil
}

func hasShortAlias(ctx context.Context, realGH string) bool {
	value, exists, err := readShortAlias(ctx, realGH)
	return err == nil && exists && value == "autoaccount"
}

func configureShortAlias(ctx context.Context, realGH string) (bool, error) {
	value, exists, err := readShortAlias(ctx, realGH)
	if err != nil {
		return false, err
	}
	if exists {
		if value == "autoaccount" {
			return false, nil
		}
		return false, errors.New("aa alias already exists")
	}
	if _, err := runAliasGH(ctx, realGH, "alias", "set", "aa", "autoaccount"); err != nil {
		return false, err
	}
	if !hasShortAlias(ctx, realGH) {
		return false, fmt.Errorf("cannot verify aa alias")
	}
	return true, nil
}
