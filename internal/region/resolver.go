// Package region provides deterministic account home-region selection. It is
// intentionally independent of transport and storage so routing can be shared
// by HTTP, gRPC, and workers.
package region

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"strings"
)

type Resolver struct {
	version string
	regions []string
}

func NewResolver(version string, regions []string) *Resolver {
	clean := make([]string, 0, len(regions))
	seen := map[string]struct{}{}
	for _, value := range regions {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			clean = append(clean, value)
		}
	}
	sort.Strings(clean)
	if len(clean) == 0 {
		clean = []string{"local"}
	}
	if version == "" {
		version = "v1"
	}
	return &Resolver{version: version, regions: clean}
}

func (r *Resolver) Version() string { return r.version }

func (r *Resolver) Resolve(issuer, subject string) string {
	value := sha256.Sum256([]byte(r.version + "\x00" + issuer + "\x00" + subject))
	index := binary.BigEndian.Uint64(value[:8]) % uint64(len(r.regions))
	return r.regions[index]
}

func (r *Resolver) Regions() []string {
	result := make([]string, len(r.regions))
	copy(result, r.regions)
	return result
}
