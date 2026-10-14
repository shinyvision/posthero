package postman

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Populate every field, including future fields added to the model. Checking
// all reference identities catches an accidentally shallow copy without
// repeating the production clone's field list in the test.
func filled(t reflect.Type, seen map[reflect.Type]int) reflect.Value {
	v := reflect.New(t).Elem()
	if t == reflect.TypeFor[json.RawMessage]() {
		v.SetBytes([]byte(`{"unknown":[1,true]}`))
		return v
	}
	if seen[t] >= 2 {
		return v
	}
	seen[t]++
	defer func() { seen[t]-- }()
	switch t.Kind() {
	case reflect.Pointer:
		v.Set(reflect.New(t.Elem()))
		v.Elem().Set(filled(t.Elem(), seen))
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			v.Field(i).Set(filled(t.Field(i).Type, seen))
		}
	case reflect.Slice:
		v.Set(reflect.MakeSlice(t, 1, 1))
		v.Index(0).Set(filled(t.Elem(), seen))
	case reflect.Map:
		v.Set(reflect.MakeMap(t))
		k := reflect.ValueOf("unknown").Convert(t.Key())
		v.SetMapIndex(k, filled(t.Elem(), seen))
	case reflect.String:
		v.SetString("value")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int:
		v.SetInt(7)
	}
	return v
}

func independent(t *testing.T, a, b reflect.Value, path string) {
	t.Helper()
	switch a.Kind() {
	case reflect.Pointer:
		if a.IsNil() {
			return
		}
		if a.Pointer() == b.Pointer() {
			t.Fatalf("shared pointer %s", path)
		}
		independent(t, a.Elem(), b.Elem(), path+".*")
	case reflect.Map:
		if a.IsNil() {
			return
		}
		if a.Pointer() == b.Pointer() {
			t.Fatalf("shared map %s", path)
		}
		it := a.MapRange()
		for it.Next() {
			independent(t, it.Value(), b.MapIndex(it.Key()), path+"[key]")
		}
	case reflect.Slice:
		if a.Len() == 0 {
			return
		}
		if a.Pointer() == b.Pointer() {
			t.Fatalf("shared slice %s", path)
		}
		for i := 0; i < a.Len(); i++ {
			independent(t, a.Index(i), b.Index(i), path+"[]")
		}
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			independent(t, a.Field(i), b.Field(i), path+"."+a.Type().Field(i).Name)
		}
	}
}

func TestClonesPreserveEveryFieldAndIsolateReferences(t *testing.T) {
	cases := []any{(*Collection)(nil), (*Item)(nil), (*Response)(nil), (*Environment)(nil), (*Auth)(nil)}
	for _, example := range cases {
		typ := reflect.TypeOf(example)
		t.Run(typ.String(), func(t *testing.T) {
			source := filled(typ, map[reflect.Type]int{})
			copy := source.MethodByName("Clone").Call(nil)[0]
			if !reflect.DeepEqual(source.Interface(), copy.Interface()) {
				t.Fatal("clone changed data")
			}
			independent(t, source, copy, typ.String())
			a, err := json.Marshal(source.Interface())
			if err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(copy.Interface())
			if err != nil {
				t.Fatal(err)
			}
			if string(a) != string(b) {
				t.Fatal("clone changed JSON representation")
			}
			if !reflect.Zero(typ).MethodByName("Clone").Call(nil)[0].IsNil() {
				t.Fatal("nil clone")
			}
		})
	}
}

func BenchmarkCollectionClone(b *testing.B) {
	c := &Collection{Info: Info{Name: "Large"}}
	for i := 0; i < 1000; i++ {
		c.Item = append(c.Item, &Item{ID: "request", Name: "Request", Request: &Request{Method: "POST", URL: ParseURL("https://example.test/{{id}}?x=1"), Body: &Body{Mode: "raw", Raw: `{"hello":"world"}`}}})
	}
	b.Run("JSON_round_trip", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			data, _ := json.Marshal(c)
			var out Collection
			_ = json.Unmarshal(data, &out)
		}
	})
	b.Run("Direct_copy", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = c.Clone()
		}
	})
}
