package core

import (
	"reflect"
	"testing"
)

func TestAccountRoutesReturnsCopy(t *testing.T) {
	inst := &Instance{accountRoutes: map[string]string{"alice": "DE"}}
	got := inst.AccountRoutes()
	got["alice"] = "NL"
	got["bob"] = "FI"
	if !reflect.DeepEqual(inst.AccountRoutes(), map[string]string{"alice": "DE"}) {
		t.Fatal("AccountRoutes exposed internal map")
	}
}
