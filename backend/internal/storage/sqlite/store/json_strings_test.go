package store

import "testing"

func TestDecodeJSONStringSliceRejectsCorruptValues(t *testing.T) {
	got, err := decodeJSONStringSlice("[]", "paths")
	if err != nil || len(got) != 0 {
		t.Fatalf("empty array = %#v err=%v", got, err)
	}
	got, err = decodeJSONStringSlice(`["a","b"]`, "paths")
	if err != nil || len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("strings = %#v err=%v", got, err)
	}
	for _, raw := range []string{"", "{", "[1]", "null", `"x"`, `{"a":1}`} {
		if _, err := decodeJSONStringSlice(raw, "paths"); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}
