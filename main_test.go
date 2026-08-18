package main

import "testing"

func TestParseArgsMultipleNamespaces(t *testing.T) {
	scope, flags, opts, nsList, err := parseArgs(
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
}

func TestParseArgsSingleNamespace(t *testing.T) {
	_, _, _, nsList, err := parseArgs([]string{"pods", "mcur", "-n", "kube-system", "-t"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if len(nsList) != 1 || nsList[0] != "kube-system" {
		t.Errorf("nsList = %v, want [kube-system]", nsList)
	}
}

func TestParseArgsNoNamespace(t *testing.T) {
	_, _, _, nsList, err := parseArgs([]string{"pods", "mcur"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if len(nsList) != 0 {
		t.Errorf("nsList = %v, want empty", nsList)
	}
}

func TestParseArgsPipeNoLongerSpecial(t *testing.T) {
	// '|' is no longer split: it stays part of a single literal token
	_, _, _, nsList, err := parseArgs([]string{"pods", "mcur", "-n", "default|redis"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if len(nsList) != 1 || nsList[0] != "default|redis" {
		t.Errorf("nsList = %v, want [default|redis] as one literal token", nsList)
	}
}

func TestParseArgsMissingValueAfterN(t *testing.T) {
	if _, _, _, _, err := parseArgs([]string{"pods", "mcur", "-n"}); err == nil {
		t.Error("expected error for -n with no value")
	}
	if _, _, _, _, err := parseArgs([]string{"pods", "mcur", "-n", "-r"}); err == nil {
		t.Error("expected error for -n followed by an option")
	}
}

func TestParseArgsOptions(t *testing.T) {
	_, _, opts, _, err := parseArgs([]string{"ns", "curl", "-A", "-r", "-b"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if len(opts) != 3 || opts[0] != "-A" || opts[1] != "-r" || opts[2] != "-b" {
		t.Errorf("opts = %v, want [-A -r -b]", opts)
	}
}

func TestParseArgsScopeAliases(t *testing.T) {
	for _, in := range []string{"pod", "pods", "po", "p"} {
		scope, _, _, _, err := parseArgs([]string{in, "mcur"})
		if err != nil {
			t.Fatalf("parseArgs(%q): %v", in, err)
		}
		if scope != "pods" {
			t.Errorf("scope(%q) = %q, want pods", in, scope)
		}
	}
	if _, _, _, _, err := parseArgs([]string{"bogus", "mcur"}); err == nil {
		t.Error("expected error for unknown scope")
	}
}
