package hooks

import (
	"encoding/json"
	"math"
	"math/big"
	"reflect"

	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
)

// Numbers. JSON numbers arrive as json.Number so their exact text survives
// to the upstream. Two narrow rules apply:
//   - comparisons for pins, enums, ranges and equality are by exact value,
//     so 9007199254740993 never equals 9007199254740992;
//   - CEL sees an integral number within int64 as an int, whatever its
//     spelling, so 1, 1.0 and 1e0 behave alike in rules.
// Nothing else about numeric spelling is interpreted.

// exactRat returns the exact value of a JSON or Go number.
func exactRat(v any) (*big.Rat, bool) {
	switch t := v.(type) {
	case json.Number:
		r, ok := new(big.Rat).SetString(string(t))
		return r, ok
	case int64:
		return new(big.Rat).SetInt64(t), true
	case int:
		return new(big.Rat).SetInt64(int64(t)), true
	case uint64:
		return new(big.Rat).SetInt(new(big.Int).SetUint64(t)), true
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return nil, false
		}
		return new(big.Rat).SetFloat64(t), true
	}
	return nil, false
}

func isNumber(v any) bool {
	switch v.(type) {
	case json.Number, int64, int, uint64, float64:
		return true
	}
	return false
}

// compareNumbers compares two numbers exactly; ok is false if either is not
// a number.
func compareNumbers(a, b any) (int, bool) {
	ra, ok1 := exactRat(a)
	rb, ok2 := exactRat(b)
	if !ok1 || !ok2 {
		return 0, false
	}
	return ra.Cmp(rb), true
}

// jsonEqual compares JSON values: numbers by exact value, strings, booleans
// and null by identity, arrays in order, objects by key. A string never
// equals a number.
func jsonEqual(a, b any) bool {
	if isNumber(a) || isNumber(b) {
		c, ok := compareNumbers(a, b)
		return ok && c == 0
	}
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			w, ok := y[k]
			if !ok || !jsonEqual(v, w) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !jsonEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	case nil:
		return b == nil
	}
	return reflect.DeepEqual(a, b)
}

// celNumber converts a JSON number for CEL: an int when the exact value is
// integral and fits int64, a double otherwise.
func celNumber(n json.Number) any {
	r, ok := new(big.Rat).SetString(string(n))
	if !ok {
		return string(n)
	}
	if r.IsInt() && r.Num().IsInt64() {
		return r.Num().Int64()
	}
	f, _ := r.Float64()
	return f
}

// adapter is the CEL type adapter: json.Number becomes an int or double,
// and nested maps and lists adapt through this adapter too.
type adapter struct{ base types.Adapter }

func (a adapter) NativeToValue(v any) ref.Val {
	switch t := v.(type) {
	case json.Number:
		return a.base.NativeToValue(celNumber(t))
	case map[string]any:
		return types.NewStringInterfaceMap(a, t)
	case []any:
		return types.NewDynamicList(a, t)
	case int:
		return types.Int(t)
	}
	return a.base.NativeToValue(v)
}

var celAdapter = adapter{base: types.DefaultTypeAdapter}
