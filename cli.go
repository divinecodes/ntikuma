package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type command string

const (
	cmdStart    command = "start"    //run in the tray
	cmdToggle   command = "toggle"   //show or hide the popup
	cmdShow     command = "show"     //show the popup
	cmdVoice    command = "voice"    //show the popup and start the dictation
	cmdHistory  command = "history"  //show chat history
	cmdSettings command = "settings" //show preference
	cmdQuit     command = "quit"     // quit the running instance
	cmdHelp     command = "help"
	cmdVersion  command = "version"
)

var commands = []command{cmdStart, cmdToggle, cmdShow, cmdVoice, cmdHistory, cmdSettings, cmdQuit, cmdHelp, cmdVersion}

// parseCommand reads the first non-flag argument. No argument means start
func parseCommand(args []string) (command, error) {
	for _, a := range args {
		a = strings.TrimLeft(strings.ToLower(strings.TrimSpace(a)), "-")
		switch a {
		case "":
			continue
		case "h":
			return cmdHelp, nil
		case "v":
			return cmdVersion, nil
		case "hidden", "background":
			return cmdStart, nil
		}
		for _, c := range commands {
			if a == string(c) {
				return c, nil
			}
		}
		return "", fmt.Errorf("unknown command %q", a)
	}
	return cmdStart, nil
}

// argsAfterProgram drop argv[0] from args a second instance forwarded
func argsAfterProgram(args []string) []string {
	if len(args) == 0 {
		return nil
	}

	return args[1:]
}

func usage() {
	name := filepath.Base(os.Args[0])
	fmt.Fprintf(os.Stderr, `Usage: %s [command]
Commands:
  start     run in the tray with the popup hidden (default)
  toggle    show or hide the popup        (bind to Super+Space)
  voice     show the popup and dictate    (bind to Super+Shift+Space)
  show      show the popup
  history   show chat history
  settings  show preferences
  quit      quit the running instance
  version   print the version

Run ./scripts/install-shortcuts.sh to bind the GNOME shortcuts.
`, name)
}
