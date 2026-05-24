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

// TestSeedChainsFirstInstall pins: a fresh home with no chains.toml gains
// the baseline file containing claude-native-only tiers for every canonical
// role. The seed is intentionally claude-native-only so the file is
// portable regardless of which backends the user later activates.
func TestSeedChainsFirstInstall(t *testing.T) {
	home := t.TempDir()

	if err := SeedChains(home); err != nil {
		t.Fatalf("SeedChains first-install returned error: %v", err)
	}

	target := filepath.Join(home, ".config", "sand", "chains.toml")
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read seeded file %s: %v", target, err)
	}

	body := string(got)

	if !strings.Contains(body, "[chains]") {
		t.Errorf("seeded chains.toml missing [chains] header")
	}
	requiredRoles := []string{
		`"ta-go-builder"`,
		`"ta-go-planning"`,
		`"ta-go-qa-falsification"`,
		`"ta-go-qa-proof"`,
		`"ta-closeout"`,
	}
	for _, want := range requiredRoles {
		if !strings.Contains(body, want) {
			t.Errorf("seeded chains.toml missing role key %s", want)
		}
	}
	// Baseline is claude-native-only — ollama / codex tier ENTRIES must NOT
	// appear in the default seed so the file works on any sand install. The
	// check looks for `backend = "X"` lines, not bare substrings, so prose
	// in comments referring to those backends does not trip the assertion.
	for _, forbidden := range []string{"ollama-local", "ollama-cloud", "codex-exec", "together-ai"} {
		needle := `backend = "` + forbidden + `"`
		if strings.Contains(body, needle) {
			t.Errorf("seeded chains.toml has tier %q; baseline must be claude-native-only", needle)
		}
	}
}

// TestSeedChainsNeverOverwrite pins the non-overwrite invariant for
// chains.toml (same contract as Seed for backends.toml).
func TestSeedChainsNeverOverwrite(t *testing.T) {
	home := t.TempDir()
	configDir := filepath.Join(home, ".config", "sand")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("setup mkdir %s: %v", configDir, err)
	}

	target := filepath.Join(configDir, "chains.toml")
	sentinel := []byte("DO NOT OVERWRITE\nuser-customized-chains = true\n")
	if err := os.WriteFile(target, sentinel, 0o644); err != nil {
		t.Fatalf("setup write %s: %v", target, err)
	}

	if err := SeedChains(home); err != nil {
		t.Fatalf("SeedChains against existing file returned error: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read after SeedChains %s: %v", target, err)
	}
	if string(got) != string(sentinel) {
		t.Fatalf("SeedChains overwrote existing file:\nwant: %q\ngot:  %q", sentinel, got)
	}
}

// TestSeedChainsEmptyHomeRejected pins: SeedChains never invents a home dir.
func TestSeedChainsEmptyHomeRejected(t *testing.T) {
	err := SeedChains("")
	if !errors.Is(err, ErrHomeRequired) {
		t.Fatalf("SeedChains(\"\") error = %v, want ErrHomeRequired", err)
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
