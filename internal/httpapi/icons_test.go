package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

func TestServerListsIncludeDiscoveredIcons(t *testing.T) {
	r := newAPIRig(t, nil)
	s := r.server()
	row, err := r.store.GetServer(context.Background(), uuid.MustParse(s.ID))
	if err != nil {
		t.Fatal(err)
	}
	icons := json.RawMessage(`[{"src":"https://example.com/icon.png","sizes":["48x48"],"theme":"light"}]`)
	if err := r.store.ObserveServerIcons(context.Background(), row, icons); err != nil {
		t.Fatal(err)
	}
	var want any
	_ = json.Unmarshal(icons, &want)
	for _, path := range []string{"/servers", "/servers/" + s.ID, "/portal"} {
		status, body := r.do(http.MethodGet, path, alice, "", nil)
		if status != http.StatusOK {
			t.Fatalf("%s: %d %s", path, status, body)
		}
		var items []map[string]any
		if path == "/servers/"+s.ID {
			var item map[string]any
			if err := json.Unmarshal([]byte(body), &item); err != nil {
				t.Fatal(err)
			}
			items = append(items, item)
		} else if err := json.Unmarshal([]byte(body), &items); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, item := range items {
			if item["id"] == s.ID {
				found = true
				if !reflect.DeepEqual(item["icons"], want) {
					t.Fatalf("%s: icons = %v, want %v", path, item["icons"], want)
				}
			}
		}
		if !found {
			t.Fatalf("%s: server not listed", path)
		}
	}
}
