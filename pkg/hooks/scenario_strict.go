package hooks

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// A test case with a misspelt key tests nothing: the key is ignored and the
// case passes for the wrong reason. So the loader is strict about the keys
// of the case format itself. It is not strict about what a case describes:
// catalogs, arguments and replies are a server's or a client's data, and
// may hold anything.

var hooksPkg = reflect.TypeOf(Scenario{}).PkgPath()

// strictKeys checks that raw, decoded as t, uses only keys t declares, and
// does the same for every nested value whose type belongs to the case
// format. Types from other packages, raw JSON, maps and `any` are data and
// are not inspected.
func strictKeys(path string, raw json.RawMessage, t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		if !formatType(t.Elem()) || !strings.HasPrefix(trimmed, "[") {
			return nil
		}
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return nil
		}
		for i, item := range items {
			if err := strictKeys(fmt.Sprintf("%s[%d]", path, i), item, t.Elem()); err != nil {
				return err
			}
		}
		return nil
	case reflect.Struct:
		if !formatType(t) || !strings.HasPrefix(trimmed, "{") {
			return nil
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil {
			return nil
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" || !f.IsExported() {
				continue
			}
			if name == "" {
				name = f.Name
			}
			fields[name] = f.Type
		}
		for key, value := range obj {
			ft, ok := fields[key]
			if !ok {
				known := make([]string, 0, len(fields))
				for k := range fields {
					known = append(known, k)
				}
				sort.Strings(known)
				return fmt.Errorf("%s: unknown key %q; the keys here are %s", path, key, strings.Join(known, ", "))
			}
			if err := strictKeys(path+"."+key, value, ft); err != nil {
				return err
			}
		}
	}
	return nil
}

// formatType reports whether a type is part of the case format, as opposed
// to data a case carries.
func formatType(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || t.PkgPath() != hooksPkg {
		return false
	}
	switch t.Name() {
	case "Rule", "Skill", "File": // stored data, checked by their own parsers
		return false
	}
	return true
}

// checkScenarioKeys applies strictKeys to every case in a file.
func checkScenarioKeys(b []byte) error {
	t := strings.TrimSpace(string(b))
	var cases []json.RawMessage
	if strings.HasPrefix(t, "[") {
		if err := json.Unmarshal([]byte(t), &cases); err != nil {
			return nil // the ordinary decode reports it
		}
	} else {
		dec := json.NewDecoder(strings.NewReader(t))
		for dec.More() {
			var one json.RawMessage
			if dec.Decode(&one) != nil {
				return nil
			}
			cases = append(cases, one)
		}
	}
	for i, c := range cases {
		var head struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(c, &head)
		where := fmt.Sprintf("case %d", i)
		if head.Name != "" {
			where = fmt.Sprintf("case %q", head.Name)
		}
		if err := strictKeys(where, c, reflect.TypeOf(Scenario{})); err != nil {
			return err
		}
	}
	return nil
}
