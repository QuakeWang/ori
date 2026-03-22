package envutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshot_MergesWorkspaceDotenvAndProcessEnv(t *testing.T) {
	workspace := t.TempDir()
	dotenv := "ORI_MODEL=from-dotenv\nDORIS_FE_PORT=9030\n"
	if err := os.WriteFile(filepath.Join(workspace, ".env"), []byte(dotenv), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	t.Setenv("ORI_MODEL", "from-process")

	env, err := Snapshot(workspace)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if got := env["ORI_MODEL"]; got != "from-process" {
		t.Fatalf("ORI_MODEL = %q, want process override", got)
	}
	if got := env["DORIS_FE_PORT"]; got != "9030" {
		t.Fatalf("DORIS_FE_PORT = %q, want dotenv value", got)
	}
}

func TestSnapshot_MissingDotenvIsAllowed(t *testing.T) {
	workspace := t.TempDir()

	env, err := Snapshot(workspace)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if env == nil {
		t.Fatal("Snapshot returned nil env")
	}
}

func TestDotenvPath(t *testing.T) {
	if got := dotenvPath("/tmp/workspace"); got != "/tmp/workspace/.env" {
		t.Fatalf("dotenvPath(workspace) = %q", got)
	}
	if got := dotenvPath(""); got != ".env" {
		t.Fatalf("dotenvPath(empty) = %q", got)
	}
}

func TestStringOr(t *testing.T) {
	env := map[string]string{
		"NON_EMPTY": "value",
		"EMPTY":     "",
	}

	if got := StringOr(env, "NON_EMPTY", "fallback"); got != "value" {
		t.Fatalf("StringOr existing = %q", got)
	}
	if got := StringOr(env, "EMPTY", "fallback"); got != "fallback" {
		t.Fatalf("StringOr empty = %q", got)
	}
	if got := StringOr(env, "MISSING", "fallback"); got != "fallback" {
		t.Fatalf("StringOr missing = %q", got)
	}
}

func TestIntOr(t *testing.T) {
	env := map[string]string{
		"OK":      "7",
		"INVALID": "oops",
	}

	got, err := IntOr(env, "OK", 3)
	if err != nil {
		t.Fatalf("IntOr existing: %v", err)
	}
	if got != 7 {
		t.Fatalf("IntOr existing = %d", got)
	}

	got, err = IntOr(env, "MISSING", 3)
	if err != nil {
		t.Fatalf("IntOr missing: %v", err)
	}
	if got != 3 {
		t.Fatalf("IntOr missing = %d", got)
	}

	if _, err := IntOr(env, "INVALID", 3); err == nil {
		t.Fatal("IntOr invalid: expected error")
	}
}
