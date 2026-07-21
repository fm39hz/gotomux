package tmux

import "github.com/fm39hz/gotomux/internal/store"

// Connector is the tmux abstraction — local, remote, or mock.
// Everything that interacts with tmux should depend on this interface.
type Connector interface {
	ListLive() ([]LiveSession, error)
	Has(name string) bool
	CurrentSession() string
	CurrentSessionPath() string
	Kill(name string) error
	Freeze(name string) (*store.Preset, error)
	Load(p *store.Preset) error
	Connect(name, cwd string) error
	ConnectPreset(p *store.Preset) error
}
