package proxy

type settings struct {
	Flags  uint32
	Server string
	Bypass string
}

type ownership struct{ Prior, Owned settings }

// A replacement proxy belongs to its new owner. If only the bypass list or
// auto-detection flag changed, preserve the user's edit and remove our endpoint.
func (j ownership) restore(current settings) (settings, bool) {
	if current.Server != j.Owned.Server {
		return current, false
	}
	next := current
	next.Server = j.Prior.Server
	if current.Flags == j.Owned.Flags {
		next.Flags = j.Prior.Flags
	}
	if current.Bypass == j.Owned.Bypass {
		next.Bypass = j.Prior.Bypass
	}
	return next, next != current
}
