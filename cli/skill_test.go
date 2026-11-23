package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/andyjmorgan/Bifrost/cli/internal/identity"
)

// neverPrint are the instructions both skills must carry: an agent consumes a secret by
// substitution and never shows it.
var neverPrint = []string{
	"**Never print a secret into the transcript**",
	"### ⚠️ Never echo the value",
	"**never appear in output**",
	"check a **side effect**",
}

func TestSkill_Bifrost(t *testing.T) {
	md := skillMD()
	for _, want := range append([]string{
		"name: credential-manager",
		"allowed-tools: Bash(bifrost:*) Bash(~/.local/bin/bifrost:*) Bash(curl:*)",
		"always consume it via `$(bifrost ...)` substitution",
		"bifrost credentials list",
		"bifrost oauth get <provider>",
		"BIFROST_ADDR=https://bifrost.donkeywork.dev/keyring",
		"`VAULT_ADDR`",
	}, neverPrint...) {
		if !strings.Contains(md, want) {
			t.Errorf("bifrost skill lacks %q", want)
		}
	}
	// The old name survives only where the text explains the move, never as a command to run.
	for _, pattern := range []string{`\$\(dwvault`, `(?m)^\s*dwvault\s`, `\|\s*dwvault\s`} {
		if m := regexp.MustCompile(pattern).FindString(md); m != "" {
			t.Errorf("bifrost skill tells the agent to run dwvault: %q", m)
		}
	}
	// It installs from Bifrost's releases, not from the Vault repository's.
	if !strings.Contains(md, "https://raw.githubusercontent.com/andyjmorgan/Bifrost/main/cli/install.sh") {
		t.Error("bifrost skill lacks the installer line")
	}
	if strings.Contains(md, "DonkeyWork-Vault") {
		t.Error("bifrost skill still points at the Vault repository")
	}
}

func TestSkill_Dwvault(t *testing.T) {
	t.Cleanup(identity.SetForTesting(identity.Dwvault()))
	md := skillMD()
	for _, want := range append([]string{
		"name: credential-manager",
		"allowed-tools: Bash(dwvault:*) Bash(~/.local/bin/dwvault:*) Bash(curl:*)",
		"always consume it via `$(dwvault ...)` substitution",
		"dwvault credentials list",
		"VAULT_ADDR=https://vault.donkeywork.dev",
	}, neverPrint...) {
		if !strings.Contains(md, want) {
			t.Errorf("dwvault skill lacks %q", want)
		}
	}
	// It no longer sends anyone to an installer that would fetch a different binary.
	if strings.Contains(md, "install.sh") {
		t.Error("dwvault skill still advertises an installer")
	}
}

// `skill install` writes the text for the name it was run as.
func TestInstallSkill_FollowsIdentity(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "skills", "credential-manager", "SKILL.md")
	if err := installSkill(dest, false, false); err != nil {
		t.Fatalf("installSkill: %v", err)
	}
	got, err := os.ReadFile(dest) //nolint:gosec // G304: a file in the test's temp dir
	if err != nil || string(got) != bifrostSkillMD {
		t.Fatalf("installed skill is not bifrost's (err=%v)", err)
	}

	t.Cleanup(identity.SetForTesting(identity.Dwvault()))
	// The bifrost text on disk differs from dwvault's bundled copy, so it is not clobbered.
	if err := installSkill(dest, false, false); err == nil {
		t.Fatal("expected a refusal to overwrite a differing skill without --force")
	}
	if err := installSkill(dest, true, false); err != nil {
		t.Fatalf("installSkill --force: %v", err)
	}
	got, err = os.ReadFile(dest) //nolint:gosec // G304: a file in the test's temp dir
	if err != nil || string(got) != dwvaultSkillMD {
		t.Fatalf("installed skill is not dwvault's (err=%v)", err)
	}
}
