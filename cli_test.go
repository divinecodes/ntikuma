package main

import "testing"

func TestParseCommand(t *testing.T) {
	cases := map[string]command{
		"":          cmdStart,
		"--hidden":  cmdStart,
		"toggle":    cmdToggle,
		"VOICE":     cmdVoice,
		"--version": cmdVersion,
		"-h":        cmdHelp,
		"settings":  cmdSettings,
	}
	for in, want := range cases {
		args := []string{}
		if in != "" {
			args = []string{in}
		}
		got, err := parseCommand(args)
		if err != nil || got != want {
			t.Errorf("parseCommand(%q) = %v, %v; want %v, nil", in, got, err, want)
		}
	}
	if _, err := parseCommand([]string{"bogus"}); err == nil {
		t.Error("bogus command failed")
	}
}

func TestArgsAfterProgram(t *testing.T) {
	if got := argsAfterProgram([]string{"/usr/bin/ntikuma", "toggle"}); len(got) != 1 || got[0] != "toggle" {
		t.Fatalf("got %v", got)
	}
	if got := argsAfterProgram(nil); got != nil {
		t.Fatalf("got %v", got)
	}
}
