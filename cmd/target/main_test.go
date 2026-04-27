package main

import (
	"reflect"
	"testing"
)

func TestSplitCSV(t *testing.T) {
	got := splitCSV(" localhost, bench.local ,,probe.local ")
	want := []string{"localhost", "bench.local", "probe.local"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitCSV = %#v, want %#v", got, want)
	}
}
