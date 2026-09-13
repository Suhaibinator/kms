package core

import "github.com/Suhaibinator/kms/internal/domain"

// A fenced stream may still be unwinding after its replacement connects. It
// must not turn one process session into two instances in the live inventory.
func deduplicateReleaseTransports(rows []domain.Subscriber) []domain.Subscriber {
	if rows == nil {
		return nil
	}
	type key struct {
		namespace                           domain.NamespaceRef
		name                                string
		schema                              uint64
		identity, client, instance, session string
	}
	seen := map[key]int{}
	out := make([]domain.Subscriber, 0, len(rows))
	for _, row := range rows {
		if row.ReleaseName == "" || row.SessionID == "" || len(row.Namespaces) != 1 {
			out = append(out, row)
			continue
		}
		k := key{row.Namespaces[0], row.ReleaseName, row.SchemaVersion, row.Identity, row.ClientName, row.InstanceID, row.SessionID}
		if i, ok := seen[k]; ok {
			if row.ConnectedAt.After(out[i].ConnectedAt) {
				out[i] = row
			}
			continue
		}
		seen[k] = len(out)
		out = append(out, row)
	}
	return out
}
