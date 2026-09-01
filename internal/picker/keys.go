package picker

import (
	"charm.land/bubbles/v2/key"
)

type uiKeyMap struct {
	Up      key.Binding
	Down    key.Binding
	Confirm key.Binding
	Quit    key.Binding
	Help    key.Binding
	Sticky  key.Binding
	Freeze  key.Binding
	Edit    key.Binding
	Unmake  key.Binding
}

var defaultKeyMap = uiKeyMap{
	Up: key.NewBinding(
		key.WithKeys("up", "ctrl+p"),
		key.WithHelp("↑/^p", "move up"),
	),
	Down: key.NewBinding(
		key.WithKeys("down", "ctrl+n"),
		key.WithHelp("↓/^n", "move down"),
	),
	Confirm: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "connect"),
	),
	Quit: key.NewBinding(
		key.WithKeys("esc", "ctrl+c"),
		key.WithHelp("esc/^c", "quit"),
	),
	Help: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "help"),
	),
	Sticky: key.NewBinding(
		key.WithKeys("ctrl+t"),
		key.WithHelp("^t", "sticky template"),
	),
	Freeze: key.NewBinding(
		key.WithKeys("ctrl+f"),
		key.WithHelp("^f", "freeze"),
	),
	Edit: key.NewBinding(
		key.WithKeys("ctrl+e"),
		key.WithHelp("^e", "edit preset"),
	),
	Unmake: key.NewBinding(
		key.WithKeys("ctrl+d"),
		key.WithHelp("^d", "remove"),
	),
}

func (k uiKeyMap) ShortHelp(it Item) []key.Binding {
	b := []key.Binding{k.Up, k.Down, k.Confirm, k.Quit, k.Help}
	if u, ok := unmakeBinding(it); ok {
		b = append(b, u)
	}
	return b
}

func (k uiKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Confirm, k.Quit},
		{k.Sticky, k.Freeze, k.Edit, k.Unmake},
	}
}

// unmakeBinding names ^d for the row under the cursor. The key is one; the row's
// existence level picks the verb, because the two operations were already
// disjoint — an Active row shadows its own Preset row in the list, so no row
// ever accepted both "kill" and "delete". Create and Zoxide rows have nothing
// materialized, so ^d is left out of the help line instead of being advertised
// with a verb it will not perform.
func unmakeBinding(it Item) (key.Binding, bool) {
	switch it.Kind {
	case KindActive:
		return key.NewBinding(
			key.WithKeys("ctrl+d"),
			key.WithHelp("^d", "kill "+truncateRunes(it.Name, 24)),
		), true
	case KindPreset:
		return key.NewBinding(
			key.WithKeys("ctrl+d"),
			key.WithHelp("^d", "del "+truncateRunes(it.Name, 24)),
		), true
	default:
		return key.Binding{}, false
	}
}

type pickKeyMap struct {
	Up      key.Binding
	Down    key.Binding
	Confirm key.Binding
	Quit    key.Binding
}

var pickKeys = pickKeyMap{
	Up: key.NewBinding(
		key.WithKeys("up", "ctrl+p"),
		key.WithHelp("↑/^p", "move up"),
	),
	Down: key.NewBinding(
		key.WithKeys("down", "ctrl+n"),
		key.WithHelp("↓/^n", "move down"),
	),
	Confirm: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "select"),
	),
	Quit: key.NewBinding(
		key.WithKeys("esc", "ctrl+c"),
		key.WithHelp("esc/^c", "quit"),
	),
}
