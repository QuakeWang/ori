package session

import "sort"

// AllowlistFromSlice converts a string slice to an allowlist map.
// nil input means "no restriction"; a non-nil empty slice means deny-all.
func AllowlistFromSlice(items []string) map[string]bool {
	if items == nil {
		return nil
	}
	allowed := make(map[string]bool, len(items))
	for _, item := range items {
		allowed[item] = true
	}
	return allowed
}

// AllowlistToSlice converts an allowlist map back to a deterministically sorted slice.
// A non-nil empty map becomes a non-nil empty slice to preserve deny-all semantics.
func AllowlistToSlice(allowed map[string]bool) []string {
	items := make([]string, 0, len(allowed))
	for item := range allowed {
		items = append(items, item)
	}
	sort.Strings(items)
	return items
}
