package cmd

import "testing"

func TestSetEnv_NewKey(t *testing.T) {
	env := []string{"HOME=/home/user", "PATH=/usr/bin"}
	result := setEnv(env, "GH_TOKEN", "tok123")

	found := false
	for _, e := range result {
		if e == "GH_TOKEN=tok123" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected GH_TOKEN=tok123 in env, got %v", result)
	}
	if len(result) != 3 {
		t.Errorf("expected 3 entries, got %d", len(result))
	}
}

func TestSetEnv_ReplaceExisting(t *testing.T) {
	env := []string{"HOME=/home/user", "GH_TOKEN=old", "PATH=/usr/bin"}
	result := setEnv(env, "GH_TOKEN", "new")

	count := 0
	for _, e := range result {
		if e == "GH_TOKEN=new" {
			count++
		}
		if e == "GH_TOKEN=old" {
			t.Errorf("old value should have been replaced")
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 GH_TOKEN entry, got %d", count)
	}
	if len(result) != 3 {
		t.Errorf("expected 3 entries (no growth), got %d", len(result))
	}
}

func TestSetEnv_EmptyEnv(t *testing.T) {
	var env []string
	result := setEnv(env, "FOO", "bar")

	if len(result) != 1 || result[0] != "FOO=bar" {
		t.Errorf("expected [FOO=bar], got %v", result)
	}
}

func TestSetEnv_ValueContainsEquals(t *testing.T) {
	env := []string{"HOME=/home/user"}
	result := setEnv(env, "TOKEN", "abc=def=ghi")

	found := false
	for _, e := range result {
		if e == "TOKEN=abc=def=ghi" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected TOKEN=abc=def=ghi in env, got %v", result)
	}
}

func TestSetEnv_MultipleVarsIndependent(t *testing.T) {
	env := []string{"HOME=/home/user"}
	env = setEnv(env, "GH_TOKEN", "tok1")
	env = setEnv(env, "GITHUB_TOKEN", "tok1")
	env = setEnv(env, "GH_AUTOPROFILE_USER", "user1")

	if len(env) != 4 {
		t.Errorf("expected 4 entries, got %d: %v", len(env), env)
	}

	expected := map[string]string{
		"HOME":                "/home/user",
		"GH_TOKEN":            "tok1",
		"GITHUB_TOKEN":        "tok1",
		"GH_AUTOPROFILE_USER": "user1",
	}
	for _, e := range env {
		for k, v := range expected {
			if e == k+"="+v {
				delete(expected, k)
			}
		}
	}
	if len(expected) > 0 {
		t.Errorf("missing expected env vars: %v", expected)
	}
}
