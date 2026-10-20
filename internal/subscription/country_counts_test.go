package subscription

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCountryCountsSortedAndUnknown(t *testing.T) {
	nodes := []Node{
		{ID: "1", Country: "DE"},
		{ID: "2", Country: "NL"},
		{ID: "3", Country: "DE"},
		{ID: "4"},
		{ID: "5", Country: "AA"},
	}
	counts, unknown := CountryCounts(nodes)
	want := []CountryCount{{Code: "AA", Nodes: 1}, {Code: "DE", Nodes: 2}, {Code: "NL", Nodes: 1}}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("counts = %+v, want %+v", counts, want)
	}
	if unknown != 1 {
		t.Fatalf("unknown = %d, want 1", unknown)
	}
}

func TestCountryCountsEmpty(t *testing.T) {
	counts, unknown := CountryCounts(nil)
	if len(counts) != 0 || unknown != 0 {
		t.Fatalf("counts = %+v unknown = %d, want empty", counts, unknown)
	}
	counts, unknown = CountryCounts([]Node{{ID: "1"}})
	if len(counts) != 0 || unknown != 1 {
		t.Fatalf("counts = %+v unknown = %d, want no counts and 1", counts, unknown)
	}
}

func TestCountryCountJSONShape(t *testing.T) {
	data, err := json.Marshal(CountryCount{Code: "DE", Nodes: 3})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"code":"DE","nodes":3}` {
		t.Fatalf("json = %s", data)
	}
}
