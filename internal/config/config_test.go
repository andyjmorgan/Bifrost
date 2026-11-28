package config

import (
	"encoding/base64"
	"slices"
	"testing"
)

func baseEnv(t *testing.T) {
	t.Setenv("MCPGW_DSN", "postgres://x")
	t.Setenv("MCPGW_MASTER_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
}

func TestConnectOffByDefault(t *testing.T) {
	baseEnv(t)
	c, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if c.ConnectEnabled || c.ConnectListenAddr != ":50051" || len(c.OIDCAudiences) != 0 {
		t.Fatalf("defaults: enabled=%v addr=%q audiences=%v", c.ConnectEnabled, c.ConnectListenAddr, c.OIDCAudiences)
	}
}

func TestConnectAndAudiencesFromEnv(t *testing.T) {
	baseEnv(t)
	t.Setenv("MCPGW_CONNECT_ENABLED", "true")
	t.Setenv("MCPGW_CONNECT_LISTEN_ADDR", "127.0.0.1:9000")
	t.Setenv("MCPGW_OIDC_AUDIENCES", " bifrost , other ,")
	c, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !c.ConnectEnabled || c.ConnectListenAddr != "127.0.0.1:9000" || !slices.Equal(c.OIDCAudiences, []string{"bifrost", "other"}) {
		t.Fatalf("got enabled=%v addr=%q audiences=%v", c.ConnectEnabled, c.ConnectListenAddr, c.OIDCAudiences)
	}
}

func TestConnectEnabledRejectsGarbage(t *testing.T) {
	baseEnv(t)
	t.Setenv("MCPGW_CONNECT_ENABLED", "yes please")
	if _, err := FromEnv(); err == nil {
		t.Fatal("a value that is not a bool was accepted")
	}
}
