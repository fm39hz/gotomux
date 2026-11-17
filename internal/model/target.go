package model

// SessionCandidate is the identity evidence needed to resolve a saved session
// action. It contains no tmux transport or persistence dependencies.
type SessionCandidate struct {
	Name     string
	Identity EntityIdentity
}

type TargetDisposition uint8

const (
	TargetCreate TargetDisposition = iota
	TargetAttach
	TargetNameCollision
	TargetConflictingOwners
)

type TargetResolution struct {
	Disposition TargetDisposition
	Owner       SessionCandidate
}

// ResolveSavedTarget resolves ownership before checking tmux's name namespace.
// A renamed logical owner takes precedence over an unrelated name collision.
func ResolveSavedTarget(name string, identity EntityIdentity, live []SessionCandidate) TargetResolution {
	result := TargetResolution{Disposition: TargetCreate}
	count := 0
	collision := false
	conflict := false
	for _, candidate := range live {
		relation := identity.CompareSession(candidate.Identity)
		conflict = conflict || relation == IdentityConflict
		if relation == IdentitySame {
			count++
			result.Owner = candidate
		}
		collision = collision || candidate.Name == name
	}
	if conflict || count > 1 {
		result.Disposition = TargetConflictingOwners
	} else if count == 1 {
		result.Disposition = TargetAttach
	} else if collision {
		result.Disposition = TargetNameCollision
	}
	return result
}
