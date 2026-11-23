package identity

import (
	"reflect"
	"testing"
)

func TestFromArgv0(t *testing.T) {
	for _, tc := range []struct {
		argv0 string
		want  string
	}{
		{"bifrost", "bifrost"},
		{"/usr/local/bin/bifrost", "bifrost"},
		{"dwvault", "dwvault"},
		{"/home/u/.local/bin/dwvault", "dwvault"},
		{"./dwvault", "dwvault"},
		{"dwvault.exe", "dwvault"},
		{"bifrost-linux-amd64", "bifrost"},
		// Only the exact compatibility name selects it.
		{"dwvault-linux-amd64", "bifrost"},
		{"dwvault2", "bifrost"},
		{"cli.test", "bifrost"},
		{"", "bifrost"},
	} {
		if got := FromArgv0(tc.argv0).Name; got != tc.want {
			t.Errorf("FromArgv0(%q).Name = %q, want %q", tc.argv0, got, tc.want)
		}
	}
}

func TestFromArgs(t *testing.T) {
	if got := fromArgs(nil).Name; got != "bifrost" {
		t.Errorf("fromArgs(nil).Name = %q, want bifrost", got)
	}
	if got := fromArgs([]string{"/opt/dwvault", "version"}).Name; got != "dwvault" {
		t.Errorf("fromArgs(dwvault).Name = %q, want dwvault", got)
	}
}

func TestBifrostIdentity(t *testing.T) {
	id := Bifrost()
	if id.DefaultAddr != "https://bifrost.donkeywork.dev/keyring" {
		t.Errorf("DefaultAddr = %q", id.DefaultAddr)
	}
	if want := []string{"BIFROST_ADDR", "VAULT_ADDR"}; !reflect.DeepEqual(id.AddrEnv, want) {
		t.Errorf("AddrEnv = %v, want %v", id.AddrEnv, want)
	}
	if id.ConfigDir != "bifrost" || id.KeyringService != "bifrost" {
		t.Errorf("storage names = %q/%q, want bifrost/bifrost", id.ConfigDir, id.KeyringService)
	}
	if !id.SelfUpdate {
		t.Error("bifrost should be allowed to self-update")
	}
	if id.Legacy == nil || id.Legacy.ConfigDir != "dwvault" || id.Legacy.KeyringService != "dwvault" {
		t.Fatalf("Legacy = %+v, want the dwvault storage names", id.Legacy)
	}
}

// The compatibility name keeps every name the DonkeyWork Vault CLI used, reads no
// predecessor, and never self-updates.
func TestDwvaultIdentity(t *testing.T) {
	id := Dwvault()
	if id.Name != "dwvault" || id.DefaultAddr != "https://vault.donkeywork.dev" {
		t.Errorf("name/addr = %q/%q", id.Name, id.DefaultAddr)
	}
	if want := []string{"VAULT_ADDR"}; !reflect.DeepEqual(id.AddrEnv, want) {
		t.Errorf("AddrEnv = %v, want %v", id.AddrEnv, want)
	}
	if id.ConfigDir != "dwvault" || id.KeyringService != "dwvault" {
		t.Errorf("storage names = %q/%q, want dwvault/dwvault", id.ConfigDir, id.KeyringService)
	}
	if id.SelfUpdate {
		t.Error("dwvault must not self-update")
	}
	if id.Legacy != nil {
		t.Errorf("Legacy = %+v, want nil", id.Legacy)
	}
}

func TestLegacyHosts(t *testing.T) {
	b := Bifrost()
	if got, want := b.LegacyHosts(BifrostAddr), []string{BifrostAddr, VaultAddr}; !reflect.DeepEqual(got, want) {
		t.Errorf("LegacyHosts(default) = %v, want %v", got, want)
	}
	if got, want := b.LegacyHosts(VaultAddr), []string{VaultAddr}; !reflect.DeepEqual(got, want) {
		t.Errorf("LegacyHosts(vault) = %v, want %v", got, want)
	}
	if got, want := b.LegacyHosts("https://vault.example"), []string{"https://vault.example"}; !reflect.DeepEqual(got, want) {
		t.Errorf("LegacyHosts(other) = %v, want %v", got, want)
	}
	if got := Dwvault().LegacyHosts(VaultAddr); got != nil {
		t.Errorf("dwvault LegacyHosts = %v, want nil", got)
	}
}

func TestSetForTesting(t *testing.T) {
	before := Current().Name
	restore := SetForTesting(Dwvault())
	if Current().Name != "dwvault" {
		t.Fatalf("Current().Name = %q after swap, want dwvault", Current().Name)
	}
	restore()
	if Current().Name != before {
		t.Fatalf("Current().Name = %q after restore, want %q", Current().Name, before)
	}
}
