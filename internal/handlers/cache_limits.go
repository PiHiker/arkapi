package handlers

import "time"

const maxHandlerCacheEntries = 512

func pruneTTLCacheEntries[K comparable, V any](items map[K]V, maxEntries int, expiresAt func(V) time.Time) {
	if len(items) == 0 {
		return
	}

	now := time.Now()
	for key, value := range items {
		if now.After(expiresAt(value)) {
			delete(items, key)
		}
	}

	for len(items) > maxEntries {
		var oldestKey K
		var oldestExpiresAt time.Time
		first := true
		for key, value := range items {
			entryExpiresAt := expiresAt(value)
			if first || entryExpiresAt.Before(oldestExpiresAt) {
				oldestKey = key
				oldestExpiresAt = entryExpiresAt
				first = false
			}
		}
		if first {
			return
		}
		delete(items, oldestKey)
	}
}
