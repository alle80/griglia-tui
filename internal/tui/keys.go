package tui

import tea "charm.land/bubbletea/v2"

type keymap struct {
	Down, Up, Open, New, Help, Back, Quit, ForceQuit []string
}

var keys = keymap{
	Down:      []string{"j", "down"},
	Up:        []string{"k", "up"},
	Open:      []string{"enter"},
	New:       []string{"n"},
	Help:      []string{"?"},
	Back:      []string{"q", "esc"},
	Quit:      []string{"Q"},
	ForceQuit: []string{"ctrl+c"},
}

func matches(msg tea.KeyPressMsg, bindings []string) bool {
	key := msg.String()
	for _, binding := range bindings {
		if key == binding {
			return true
		}
	}
	return false
}
