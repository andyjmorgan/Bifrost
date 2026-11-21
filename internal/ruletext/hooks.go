package ruletext

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Hooks is a server's rules as they are stored and as the API sends
// them: the text of each hook's rules, under a property that does not change.
// A nil field means the hook has no rules. The engine's rules are compiled
// from this text; nothing else is kept.
//
// A hook's property is its name with the separators dropped and the parts
// joined in camel case: tools/call.post.result is toolsCallPostResult.
// Property gives it, and a test holds every field to that.
type Hooks struct {
	ServerSettings     *string `json:"serverSettings"`
	ServerSession      *string `json:"serverSession"`
	ServerDiscoverPost *string `json:"serverDiscoverPost"`

	ToolsListPost         *string `json:"toolsListPost"`
	ToolsCallPre          *string `json:"toolsCallPre"`
	ToolsCallPostProgress *string `json:"toolsCallPostProgress"`
	ToolsCallPostElicit   *string `json:"toolsCallPostElicit"`
	ToolsCallPostResult   *string `json:"toolsCallPostResult"`
	ToolsCallPostTask     *string `json:"toolsCallPostTask"`

	PromptsListPost        *string `json:"promptsListPost"`
	PromptsGetPre          *string `json:"promptsGetPre"`
	PromptsGetPostProgress *string `json:"promptsGetPostProgress"`
	PromptsGetPostElicit   *string `json:"promptsGetPostElicit"`

	ResourcesListPost          *string `json:"resourcesListPost"`
	ResourcesTemplatesListPost *string `json:"resourcesTemplatesListPost"`
	ResourcesReadPre           *string `json:"resourcesReadPre"`
	ResourcesReadPostProgress  *string `json:"resourcesReadPostProgress"`
	ResourcesReadPostElicit    *string `json:"resourcesReadPostElicit"`
	ResourcesReadPostResult    *string `json:"resourcesReadPostResult"`
	SkillsListPost             *string `json:"skillsListPost"`
}

// Property is the property a hook's rules are kept under.
func Property(hook string) string {
	parts := strings.FieldsFunc(hook, func(r rune) bool { return r == '/' || r == '.' })
	for i := 1; i < len(parts); i++ {
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}

// fieldOf is the index of the Hooks field for each property.
var fieldOf = func() map[string]int {
	out := map[string]int{}
	t := reflect.TypeOf(Hooks{})
	for i := 0; i < t.NumField(); i++ {
		out[t.Field(i).Tag.Get("json")] = i
	}
	return out
}()

// ByName returns the text keyed by hook name, for the hooks that have rules.
func (h Hooks) ByName() map[string]string {
	out := map[string]string{}
	v := reflect.ValueOf(h)
	for _, s := range all {
		if p := v.Field(fieldOf[s.Property]).Interface().(*string); p != nil && strings.TrimSpace(*p) != "" {
			out[s.Name] = *p
		}
	}
	return out
}

// Empty reports whether no hook has rules.
func (h Hooks) Empty() bool { return len(h.ByName()) == 0 }

// Stored is the form kept in the database: an object with only the
// properties that have rules.
func (h Hooks) Stored() json.RawMessage {
	out := map[string]string{}
	for hook, t := range h.ByName() {
		out[Property(hook)] = t
	}
	b, _ := json.Marshal(out)
	return b
}

// UnmarshalJSON reads the object strictly: a property that is not a hook's
// is refused, so a misspelt one cannot pass for "no rules", and so is text
// the database cannot hold.
func (h *Hooks) UnmarshalJSON(b []byte) error {
	var raw map[string]*string
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("hooks must be an object of rule text by hook property: %w", err)
	}
	*h = Hooks{}
	v := reflect.ValueOf(h).Elem()
	var unknown []string
	for prop, t := range raw {
		i, ok := fieldOf[prop]
		if !ok {
			unknown = append(unknown, prop)
			continue
		}
		if t == nil || strings.TrimSpace(*t) == "" {
			continue
		}
		if strings.ContainsRune(*t, 0) {
			return fmt.Errorf("%s holds a NUL character", prop)
		}
		v.Field(i).Set(reflect.ValueOf(t))
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("hooks has no property %s", strings.Join(unknown, ", "))
	}
	return nil
}

// ParseStored reads what Stored wrote. Nothing, null and {} are no rules.
func ParseStored(raw json.RawMessage) (Hooks, error) {
	var h Hooks
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || string(t) == "null" {
		return h, nil
	}
	err := json.Unmarshal(t, &h)
	return h, err
}

// ProblemsError joins problems into one message: tools/call.pre line 2: ….
func ProblemsError(probs []Problem) error {
	if len(probs) == 0 {
		return nil
	}
	parts := make([]string, 0, len(probs))
	for _, p := range probs {
		switch {
		case p.Hook == "":
			parts = append(parts, p.Message)
		case p.Line == 0:
			parts = append(parts, p.Hook+": "+p.Message)
		default:
			parts = append(parts, fmt.Sprintf("%s line %d: %s", p.Hook, p.Line, p.Message))
		}
	}
	return fmt.Errorf("%s", strings.Join(parts, "; "))
}
