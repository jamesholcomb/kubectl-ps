package main

import (
	"reflect"
	"testing"
)

func TestParseArgsMultipleNamespaces(t *testing.T) {
	scope, flags, opts, nsList, statuses, err := parseArgs(
		[]string{"nodes", "mcp", "-n", "default", "redis", "mongodb", "-r", "-h"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if scope != "nodes" {
		t.Errorf("scope = %q, want %q", scope, "nodes")
	}
	if flags != "mcp" {
		t.Errorf("flags = %q, want %q", flags, "mcp")
	}
	want := []string{"default", "redis", "mongodb"}
	if len(nsList) != len(want) {
		t.Fatalf("nsList = %v, want %v", nsList, want)
	}
	for i, w := range want {
		if nsList[i] != w {
			t.Errorf("nsList[%d] = %q, want %q", i, nsList[i], w)
		}
	}
	if len(opts) != 2 || opts[0] != "-r" || opts[1] != "-h" {
		t.Errorf("opts = %v, want [-r -h]", opts)
	}
	if len(statuses) != 0 {
		t.Errorf("statuses = %v, want empty", statuses)
	}
}

func TestParseArgsSingleNamespace(t *testing.T) {
	_, _, _, nsList, _, err := parseArgs([]string{"pods", "mcur", "-n", "kube-system", "-t"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if len(nsList) != 1 || nsList[0] != "kube-system" {
		t.Errorf("nsList = %v, want [kube-system]", nsList)
	}
}

func TestParseArgsNoNamespace(t *testing.T) {
	_, _, _, nsList, _, err := parseArgs([]string{"pods", "mcur"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if len(nsList) != 0 {
		t.Errorf("nsList = %v, want empty", nsList)
	}
}

func TestParseArgsPipeNoLongerSpecial(t *testing.T) {
	// '|' is no longer split: it stays part of a single literal token
	_, _, _, nsList, _, err := parseArgs([]string{"pods", "mcur", "-n", "default|redis"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if len(nsList) != 1 || nsList[0] != "default|redis" {
		t.Errorf("nsList = %v, want [default|redis] as one literal token", nsList)
	}
}

func TestParseArgsMissingValueAfterN(t *testing.T) {
	if _, _, _, _, _, err := parseArgs([]string{"pods", "mcur", "-n"}); err == nil {
		t.Error("expected error for -n with no value")
	}
	if _, _, _, _, _, err := parseArgs([]string{"pods", "mcur", "-n", "-r"}); err == nil {
		t.Error("expected error for -n followed by an option")
	}
}

func TestParseArgsOptions(t *testing.T) {
	_, _, opts, _, _, err := parseArgs([]string{"ns", "curl", "-A", "-r", "-b"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if len(opts) != 3 || opts[0] != "-A" || opts[1] != "-r" || opts[2] != "-b" {
		t.Errorf("opts = %v, want [-A -r -b]", opts)
	}
}

func TestParseArgsScopeAliases(t *testing.T) {
	for _, in := range []string{"pod", "pods", "po", "p"} {
		scope, _, _, _, _, err := parseArgs([]string{in, "mcur"})
		if err != nil {
			t.Fatalf("parseArgs(%q): %v", in, err)
		}
		if scope != "pods" {
			t.Errorf("scope(%q) = %q, want pods", in, scope)
		}
	}
	if _, _, _, _, _, err := parseArgs([]string{"bogus", "mcur"}); err == nil {
		t.Error("expected error for unknown scope")
	}
}

func TestParseArgsMultipleStatuses(t *testing.T) {
	_, _, opts, _, statuses, err := parseArgs(
		[]string{"pods", "mcur", "-s", "running", "succeeded", "-r"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	want := []string{"running", "succeeded"}
	if !reflect.DeepEqual(statuses, want) {
		t.Errorf("statuses = %v, want %v", statuses, want)
	}
	if len(opts) != 1 || opts[0] != "-r" {
		t.Errorf("opts = %v, want [-r]", opts)
	}
}

func TestParseArgsStatusesAndNamespaces(t *testing.T) {
	_, _, _, nsList, statuses, err := parseArgs(
		[]string{"pods", "mcur", "-n", "default", "kube-system", "-s", "running", "-t"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if !reflect.DeepEqual(nsList, []string{"default", "kube-system"}) {
		t.Errorf("nsList = %v, want [default kube-system]", nsList)
	}
	if !reflect.DeepEqual(statuses, []string{"running"}) {
		t.Errorf("statuses = %v, want [running]", statuses)
	}
}

func TestParseArgsMissingValueAfterS(t *testing.T) {
	if _, _, _, _, _, err := parseArgs([]string{"pods", "mcur", "-s"}); err == nil {
		t.Error("expected error for -s with no value")
	}
	if _, _, _, _, _, err := parseArgs([]string{"pods", "mcur", "-s", "-r"}); err == nil {
		t.Error("expected error for -s followed by an option")
	}
}

func TestCanonicalStatuses(t *testing.T) {
	got, err := canonicalStatuses([]string{"running", "SUCCEEDED", "Failed", "peNdInG"})
	if err != nil {
		t.Fatalf("canonicalStatuses: %v", err)
	}
	want := []string{"Running", "Succeeded", "Failed", "Pending"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("canonicalStatuses = %v, want %v", got, want)
	}
}

func TestCanonicalStatusesUnknown(t *testing.T) {
	if _, err := canonicalStatuses([]string{"running", "bogus"}); err == nil {
		t.Error("expected error for unknown status")
	}
}

func TestDefaultStatuses(t *testing.T) {
	if got := defaultStatuses(nil); !reflect.DeepEqual(got, []string{"Succeeded"}) {
		t.Errorf("defaultStatuses(nil) = %v, want [Succeeded]", got)
	}
	if got := defaultStatuses([]string{"Running"}); !reflect.DeepEqual(got, []string{"Running"}) {
		t.Errorf("defaultStatuses([Running]) = %v, want [Running]", got)
	}
}

func TestPhaseSelector(t *testing.T) {
	if got := phaseSelector([]string{"Succeeded"}); got != "status.phase=Succeeded" {
		t.Errorf("phaseSelector = %q, want %q", got, "status.phase=Succeeded")
	}
	if got := phaseSelector([]string{"Succeeded", "Running"}); got != "status.phase=Succeeded,Running" {
		t.Errorf("phaseSelector = %q, want %q", got, "status.phase=Succeeded,Running")
	}
}
