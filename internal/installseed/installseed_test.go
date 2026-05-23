package installseed

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInstallSeedFirstInstall pins: a fresh home with no backends.toml gains
// the baseline file containing the active claude-native block plus four
// commented example provider blocks.
func TestInstallSeedFirstInstall(t *testing.T) {
	home := t.TempDir()

	if err := Seed(home); err != nil {
		t.Fatalf("Seed first-install returned error: %v", err)
	}

	target := filepath.Join(home, ".config", "sand", "backends.toml")
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read seeded file %s: %v", target, err)
	}

	body := string(got)

	// Active claude-native block must be present AND uncommented.
	if !strings.Contains(body, "\n[backends.claude-native]\n") {
		t.Errorf("seeded file missing uncommented [backends.claude-native] header")
	}

	// Each of the four examples must be present as COMMENTED blocks.
	// Headers are prefixed with `# ` per the seed template.
	commentedExamples := []string{
		"# [backends.codex-exec]",
		"# [backends.ollama-local]",
		"# [backends.ollama-cloud]",
		"# [backends.together-ai]",
	}
	for _, want := range commentedExamples {
		if !strings.Contains(body, want) {
			t.Errorf("seeded file missing commented example header %q", want)
		}
	}

	// The active block must not also appear in commented form (sanity:
	// guards against accidentally commenting out the baseline).
	if strings.Contains(body, "# [backends.claude-native]") {
		t.Errorf("active [backends.claude-native] block also appears commented; baseline must be uncommented only")
	}
}

// TestInstallSeedNeverOverwrite pins the non-overwrite invariant: any
// pre-existing backends.toml is preserved byte-for-byte regardless of its
// content — including content that does not resemble the seed at all.
func TestInstallSeedNeverOverwrite(t *testing.T) {
	home := t.TempDir()
	configDir := filepath.Join(home, ".config", "sand")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("setup mkdir %s: %v", configDir, err)
	}

	target := filepath.Join(configDir, "backends.toml")
	sentinel := []byte("DO NOT OVERWRITE\nuser-customized-content = true\n")
	if err := os.WriteFile(target, sentinel, 0o644); err != nil {
		t.Fatalf("setup write %s: %v", target, err)
	}

	if err := Seed(home); err != nil {
		t.Fatalf("Seed against existing file returned error: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read after Seed %s: %v", target, err)
	}
	if string(got) != string(sentinel) {
		t.Fatalf("Seed overwrote existing file:\nwant: %q\ngot:  %q", sentinel, got)
	}
}

// TestInstallSeedEmptyHomeRejected pins: Seed never invents a home dir. An
// empty home argument is a programming error and returns ErrHomeRequired.
func TestInstallSeedEmptyHomeRejected(t *testing.T) {
	err := Seed("")
	if !errors.Is(err, ErrHomeRequired) {
		t.Fatalf("Seed(\"\") error = %v, want ErrHomeRequired", err)
	}
}

// TestInstallSeedCreatesNestedDir pins: when ~/.config/sand does not exist,
// Seed creates it (along with ~/.config when needed). Covers the
// fresh-home-no-config-dir case.
func TestInstallSeedCreatesNestedDir(t *testing.T) {
	home := t.TempDir()
	// Confirm precondition: ~/.config/sand does not exist yet.
	if _, err := os.Stat(filepath.Join(home, ".config", "sand")); !os.IsNotExist(err) {
		t.Fatalf("precondition: expected ~/.config/sand to be absent, stat err = %v", err)
	}

	if err := Seed(home); err != nil {
		t.Fatalf("Seed returned error: %v", err)
	}

	info, err := os.Stat(filepath.Join(home, ".config", "sand"))
	if err != nil {
		t.Fatalf("post-Seed stat ~/.config/sand: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("~/.config/sand exists but is not a directory")
	}
}
