package modelcatalog

import (
	"reflect"
	"testing"
)

func TestOpenCodeModelEffortsFollowEachModel(t *testing.T) {
	raw := []byte(`p/one
{"id":"one","providerID":"p","name":"One","variants":{"low":{},"max":{"nested":{"x":"}"}}}}
p/two
{"id":"two","providerID":"p","name":"Two","variants":{"high":{}}}
`)
	got, err := parseOpenCodeModels(raw)
	if err != nil || len(got) != 2 {
		t.Fatal(got, err)
	}
	if !reflect.DeepEqual(got[0].Efforts, []string{"low", "max"}) || !reflect.DeepEqual(got[1].Efforts, []string{"high"}) {
		t.Fatal(got)
	}
	for _, bad := range []string{"p/one", "p/one\n{", "p/one\n{\"id\":\"other\",\"providerID\":\"p\"}"} {
		if _, err := parseOpenCodeModels([]byte(bad)); err == nil {
			t.Fatal("accepted malformed catalog", bad)
		}
	}
}
