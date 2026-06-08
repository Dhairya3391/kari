package provider

import "sort"

// Registry stores registered providers and returns providers by mode.
type Registry struct {
	providers []Provider
}

// Register adds a provider to the registry. It is idempotent.
func (r *Registry) Register(p Provider) {
	for _, existing := range r.providers {
		if existing.Name() == p.Name() {
			return
		}
	}
	r.providers = append(r.providers, p)
}

// ProvidersForMode returns providers supporting the given mode, ordered by priority.
func (r *Registry) ProvidersForMode(mode ContentType) []Provider {
	var matched []Provider
	for _, p := range r.providers {
		for _, m := range p.Modes() {
			if m.Name == mode {
				matched = append(matched, p)
				break
			}
		}
	}

	sort.Slice(matched, func(i, j int) bool {
		priorityI := 100
		for _, m := range matched[i].Modes() {
			if m.Name == mode {
				priorityI = m.Priority
				break
			}
		}
		priorityJ := 100
		for _, m := range matched[j].Modes() {
			if m.Name == mode {
				priorityJ = m.Priority
				break
			}
		}
		return priorityI < priorityJ
	})

	return matched
}

// AllModes returns the sorted list of unique modes supported by registered providers.
func (r *Registry) AllModes() []ContentType {
	modeSet := make(map[ContentType]struct{})
	for _, p := range r.providers {
		for _, m := range p.Modes() {
			modeSet[m.Name] = struct{}{}
		}
	}

	var modes []ContentType
	for m := range modeSet {
		modes = append(modes, m)
	}
	sort.Slice(modes, func(i, j int) bool {
		return string(modes[i]) < string(modes[j])
	})
	return modes
}
