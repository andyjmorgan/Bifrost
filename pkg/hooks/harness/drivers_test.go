package harness_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/andyjmorgan/Bifrost/pkg/hooks/harness"
	"github.com/andyjmorgan/Bifrost/pkg/hooks/harness/clients"
)

// This file is the seam between the language drivers in the clients package
// and the harness's Client interface. It is the only place a clients.Driver
// is adapted, and the only place the drivers are registered. It lives in the
// external test package because clients imports harness.
//
// Each driver is a subprocess. Starting one per request would be slow, so a
// small pool of processes per language is started on first use and reused
// for the whole test binary; TestMain closes them.

// drivers is every language driver the cross-client tests run with. To add
// one, add a line.
var drivers = []struct {
	name string
	open func() (clients.Driver, error)
	// identity is the client name the driver program declares.
	identity string
	// noMeta marks a driver whose call does not pass a request's _meta on.
	noMeta bool
}{
	{"python-sdk", clients.NewPython, "harness-python-client", true},
	{"typescript-sdk", clients.NewTypeScript, "harness-typescript-client", false},
}

// poolSize is how many processes each language driver may use at once.
const poolSize = 4

var opened []*driverClient

func init() {
	for _, d := range drivers {
		c, err := newDriverClient(d.name, d.open)
		if err == nil {
			c.noMeta, c.identity = d.noMeta, d.identity
		}
		if err != nil {
			// Loud, and by name: a missing runtime must not look like a pass.
			fmt.Fprintf(os.Stderr, "harness: CLIENT NOT RUN: %s: %v\n", d.name, err)
			harness.SkippedClients = append(harness.SkippedClients, d.name+": "+err.Error())
			continue
		}
		opened = append(opened, c)
		harness.Clients = append(harness.Clients, c)
	}
}

func TestMain(m *testing.M) {
	code := m.Run()
	for _, c := range opened {
		c.close()
	}
	os.Exit(code)
}

// driverClient adapts a clients.Driver to harness.Client.
type driverClient struct {
	name     string
	open     func() (clients.Driver, error)
	noMeta   bool
	identity string

	mu   sync.Mutex
	all  []clients.Driver
	free chan clients.Driver
}

func newDriverClient(name string, open func() (clients.Driver, error)) (*driverClient, error) {
	first, err := open() // fails here when the runtime or the SDK is missing
	if err != nil {
		return nil, err
	}
	c := &driverClient{name: name, open: open, free: make(chan clients.Driver, poolSize)}
	c.all = append(c.all, first)
	c.free <- first
	for i := 1; i < poolSize; i++ {
		d, err := open()
		if err != nil {
			break
		}
		c.all = append(c.all, d)
		c.free <- d
	}
	return c, nil
}

func (c *driverClient) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, d := range c.all {
		_ = d.Close()
	}
}

func (c *driverClient) Name() string     { return c.name }
func (c *driverClient) Identity() string { return c.identity }

func (c *driverClient) Can(req harness.ClientRequest) string {
	if why := harness.SDKShape(req); why != "" {
		return why
	}
	if req.Capabilities != nil {
		if _, ok := req.Capabilities.(map[string]any); !ok {
			return "capabilities that are not an object"
		}
	}
	if len(req.ClientInfo) > 0 {
		return "a client identity this driver cannot declare"
	}
	if _, has := req.Params["_meta"]; has && c.noMeta {
		return "a request _meta, which this driver's call does not pass on"
	}
	if inexactNumber(req.RawParams) {
		return "a number that does not survive the driver's JSON plumbing, which reads numbers as 64-bit floats"
	}
	return ""
}

// inexactNumber reports whether a JSON value holds a number a 64-bit float
// cannot carry exactly. The line protocol to the driver processes, and
// JavaScript itself, read numbers as floats.
func inexactNumber(raw json.RawMessage) bool {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	for {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		n, ok := tok.(json.Number)
		if !ok {
			continue
		}
		exact, _, err := big.ParseFloat(n.String(), 10, 2000, big.ToNearestEven)
		if err != nil {
			return true
		}
		f, _ := exact.Float64()
		if new(big.Float).SetPrec(2000).SetFloat64(f).Cmp(exact) != 0 {
			return true
		}
	}
}

func (c *driverClient) Do(ctx context.Context, endpoint string, req harness.ClientRequest) (*harness.ClientReply, error) {
	d := <-c.free
	defer func() { c.free <- d }()

	caps, _ := req.Capabilities.(map[string]any)
	noCache := false
	out := &harness.ClientReply{}
	conn, err := d.Connect(ctx, clients.ConnectOptions{URL: endpoint, Token: req.Token, Capabilities: caps, Cache: &noCache})
	if err != nil {
		return nil, err
	}
	out.SDK = map[string]any{"name": conn.SDK.Name, "version": conn.SDK.Version, "language": conn.SDK.Language, "protocolVersion": conn.SDK.ProtocolVersion}
	if conn.Error != nil {
		out.NotConnected = true
		out.Failure = "connect: " + conn.Error.Error()
		return out, nil
	}
	call := clients.CallOptions{Method: req.Method, Params: req.Params, Progress: req.Progress}
	for _, a := range req.Elicit {
		answer := clients.Answer{}
		answer.Action, _ = a["action"].(string)
		answer.Content, _ = a["content"].(map[string]any)
		call.Elicit = append(call.Elicit, answer)
	}
	rep, err := d.Call(ctx, call)
	notSent := err != nil && strings.Contains(err.Error(), "does not advertise")
	for _, note := range rep.Notes {
		if strings.Contains(note, "does not advertise") {
			notSent = true
		}
	}
	if notSent {
		// The TypeScript SDK will not send a request for a capability the
		// server's discovery did not advertise. The request was never made.
		out.Refused = "the SDK will not ask for a capability the server does not advertise"
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	out.Progress, out.Elicitations = rep.Progress, rep.Elicitations
	// A question beyond the script: the driver's handler refused it, and
	// the SDK stopped the call there.
	out.Unanswered = len(rep.Elicitations) > len(req.Elicit)
	switch {
	case rep.Error != nil && rep.Error.Kind == "rpc":
		ce := &harness.ClientError{Message: rep.Error.Message, Data: rep.Error.Data}
		ce.Code = int64(rep.Error.RPCCode())
		out.Error = ce
	case rep.Error != nil:
		out.Failure = rep.Error.Kind + ": " + rep.Error.Message
	default:
		// Keep the shape the other clients give: a generic JSON value.
		b, _ := json.Marshal(rep.Result)
		var v any
		_ = json.Unmarshal(b, &v)
		out.Result = v
	}
	return out, nil
}
