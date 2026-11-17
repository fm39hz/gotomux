package tmux

import (
	"context"

	"github.com/fm39hz/gotomux/internal/model"
)

type Connector interface {
	ListLive(ctx context.Context) ([]LiveSession, error)
	Has(ctx context.Context, name string) bool
	CurrentSession(ctx context.Context) string
	CurrentSessionPath(ctx context.Context) string
	// CurrentContext resolves session name and path in one fork instead of two.
	CurrentContext(ctx context.Context) (session, path string)
	Kill(ctx context.Context, name string) error
	Freeze(ctx context.Context, name string) (*model.Session, error)
	Load(ctx context.Context, p *model.Session) error
	Connect(ctx context.Context, name, cwd string) error
}

// ObservedConnector attaches only to an existing runtime session on the
// observed server generation. It must never create a session as a fallback.
type ObservedConnector interface {
	ConnectObserved(ctx context.Context, runtimeID, serverKey string) error
}

// CreatedSessionCleaner is used only with the receipt of a successful create.
// Removal is guarded by the server generation and exact runtime handle.
type CreatedSessionCleaner interface {
	RemoveCreatedSession(ctx context.Context, runtimeID, serverKey string) error
}

// ObservedSessionRemover removes exactly the selected runtime incarnation.
// Implementations must reject missing or stale server evidence.
type ObservedSessionRemover interface {
	KillObserved(ctx context.Context, runtimeID, serverKey string) error
}
