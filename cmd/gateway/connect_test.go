package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/andyjmorgan/Bifrost/internal/auth"
	"github.com/andyjmorgan/Bifrost/internal/config"
	"github.com/andyjmorgan/Bifrost/internal/store"
	bifrostv1 "github.com/andyjmorgan/Bifrost/pkg/api/bifrost/v1"
	"github.com/andyjmorgan/Bifrost/pkg/api/bifrost/v1/bifrostv1connect"
)

// oneServer is a store holding one server, granted to "dev".
type oneServer struct{}

func (oneServer) ListGrantedWithCache(_ context.Context, userID string, _ ...string) ([]*store.Server, map[uuid.UUID]store.CacheBundle, error) {
	if userID != "dev" {
		return nil, nil, nil
	}
	return []*store.Server{{ID: uuid.New(), Slug: "search", Enabled: true}}, nil, nil
}

func (oneServer) ListCapabilities(context.Context, uuid.UUID) ([]*store.Capability, error) {
	return nil, nil
}

func (oneServer) EnsureUser(_ context.Context, id, email, name string) (*store.User, error) {
	return &store.User{ID: id, Email: email, Name: name}, nil
}

func (oneServer) GetUser(_ context.Context, id string) (*store.User, error) {
	return &store.User{ID: id}, nil
}

// freeAddr returns a loopback address nothing is listening on.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func startConnectFor(t *testing.T, cfg config.Config) net.Addr {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	authn := &auth.Authenticator{Users: oneServer{}, DevOwner: "dev"} // dev mode
	addr, err := startConnect(ctx, cfg, authn, oneServer{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return addr
}

// With MCPGW_CONNECT_ENABLED unset, nothing listens on the Connect address.
func TestConnectOffListensNowhere(t *testing.T) {
	addr := freeAddr(t)
	if got := startConnectFor(t, config.Config{ConnectEnabled: false, ConnectListenAddr: addr}); got != nil {
		t.Fatalf("connect off, but listening on %v", got)
	}
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err == nil {
		_ = conn.Close()
		t.Fatalf("connect off, but %s accepts connections", addr)
	}
}

func TestConnectOnServesPortal(t *testing.T) {
	addr := startConnectFor(t, config.Config{ConnectEnabled: true, ConnectListenAddr: freeAddr(t), PublicBaseURL: "https://gw.example"})
	if addr == nil {
		t.Fatal("connect on, but not listening")
	}
	c := bifrostv1connect.NewPortalServiceClient(http.DefaultClient, "http://"+addr.String())
	res, err := c.ListServers(context.Background(), connect.NewRequest(&bifrostv1.ListServersRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Msg.Servers) != 1 || res.Msg.Servers[0].McpUrl != "https://gw.example/search/mcp" {
		t.Fatalf("servers: %v", res.Msg.Servers)
	}
}
