package ipc

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The structs in messages.go carry the schema rules of ipc/v1 that encoding/json does not check:
//   - a field without omitzero is required (it must be present);
//   - a required pointer is the nullable one (T|null); an optional field may be absent but never null;
//   - the ipc tag holds enum=a|b, min=N, max=N, minlen=N, minitems=N and pattern=hex16|hex32|hex64.
// checkShape enforces them on the decoded JSON value before it is decoded into the struct. In strict mode (tests) it
// also refuses a key that is not one of the struct's exact JSON names (encoding/json matches names case-insensitively).

var unmarshalerType = reflect.TypeFor[json.Unmarshaler]()

var patterns = map[string]*regexp.Regexp{
	"hex16": regexp.MustCompile(`^[0-9a-f]{16}$`),
	"hex32": regexp.MustCompile(`^[0-9a-f]{32}$`),
	"hex64": regexp.MustCompile(`^[0-9a-f]{64}$`),
}

// decodesItself: a type with its own UnmarshalJSON (Nullable, json.RawMessage, BudgetScope) checks its own value.
func decodesItself(t reflect.Type) bool { return reflect.PointerTo(t).Implements(unmarshalerType) }

func checkShape(v any, t reflect.Type, path string, strict bool) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if decodesItself(t) {
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: must be an object", path)
		}
		known := make(map[string]bool, t.NumField())
		for i := range t.NumField() {
			f := t.Field(i)
			name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" || name == "-" {
				continue
			}
			known[name] = true
			p := path + "." + name
			optional := opts == "omitzero"
			val, present := obj[name]
			if !present {
				if optional {
					continue
				}
				return fmt.Errorf("%s: required", p)
			}
			if val == nil {
				if decodesItself(f.Type) || (f.Type.Kind() == reflect.Pointer && !optional) {
					continue
				}
				return fmt.Errorf("%s: must not be null", p)
			}
			if err := checkRules(val, f.Tag.Get("ipc"), p); err != nil {
				return err
			}
			if err := checkShape(val, f.Type, p, strict); err != nil {
				return err
			}
		}
		if strict {
			for k := range obj {
				if !known[k] {
					return fmt.Errorf("%s.%s: unknown field", path, k)
				}
			}
		}
	case reflect.Slice:
		arr, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s: must be an array", path)
		}
		elem := t.Elem()
		for i, x := range arr {
			p := fmt.Sprintf("%s[%d]", path, i)
			if x == nil && elem.Kind() != reflect.Interface && !decodesItself(elem) {
				return fmt.Errorf("%s: must not be null", p)
			}
			if err := checkShape(x, elem, p, strict); err != nil {
				return err
			}
		}
	case reflect.Map:
		obj, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: must be an object", path)
		}
		elem := t.Elem()
		for k, x := range obj {
			if x == nil && elem.Kind() != reflect.Interface {
				return fmt.Errorf("%s.%s: must not be null", path, k)
			}
			if err := checkShape(x, elem, path+"."+k, strict); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkRules applies one field's ipc tag to its decoded JSON value. A rule meant for another JSON type is skipped:
// a wrong type is the decoder's error.
func checkRules(v any, tag, path string) error {
	if tag == "" {
		return nil
	}
	for _, rule := range strings.Split(tag, ",") {
		key, arg, _ := strings.Cut(rule, "=")
		switch key {
		case "enum":
			s := fmt.Sprint(v)
			if !slices.Contains(strings.Split(arg, "|"), s) {
				return fmt.Errorf("%s: %q is not one of %s", path, s, arg)
			}
		case "min", "max":
			n, ok := v.(float64)
			if !ok {
				continue
			}
			lim, err := strconv.ParseFloat(arg, 64)
			if err != nil {
				return fmt.Errorf("%s: bad rule %q", path, rule)
			}
			if (key == "min" && n < lim) || (key == "max" && n > lim) {
				return fmt.Errorf("%s: %v is out of range (%s %s)", path, n, key, arg)
			}
		case "minlen":
			s, ok := v.(string)
			if ok && utf8.RuneCountInString(s) < atoi(arg) {
				return fmt.Errorf("%s: must be at least %s characters", path, arg)
			}
		case "minitems":
			a, ok := v.([]any)
			if ok && len(a) < atoi(arg) {
				return fmt.Errorf("%s: must have at least %s items", path, arg)
			}
		case "pattern":
			re := patterns[arg]
			if re == nil {
				return fmt.Errorf("%s: unknown pattern %q", path, arg)
			}
			if s, ok := v.(string); ok && !re.MatchString(s) {
				return fmt.Errorf("%s: must match %s", path, re)
			}
		default:
			return fmt.Errorf("%s: unknown rule %q", path, rule)
		}
	}
	return nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
