package core

import (
	"reflect"
	"testing"
)

func TestAccountRoutesReturnsCopy(t *testing.T) {
	inst := instanceWith(&routingState{accountRoutes: map[string]string{"alice": "de"}})
	got := inst.AccountRoutes()
	got["alice"] = "NL"
	got["bob"] = "FI"
	if !reflect.DeepEqual(inst.AccountRoutes(), map[string]string{"alice": "de"}) {
		t.Fatal("AccountRoutes exposed internal map")
	}
}
