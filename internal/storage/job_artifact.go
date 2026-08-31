package storage

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/aero/aero-id/internal/domain"
)

// JobArtifactKey is stable for one lease generation. A retry can overwrite its
// own artifact, while an expired worker's late write lands in a separate object
// and cannot overwrite the replacement attempt.
func JobArtifactKey(owner string, jobID domain.ID, kind string) (string, error) {
	owner = strings.TrimSpace(owner)
	kind = strings.TrimSpace(kind)
	if owner == "" || strings.ContainsAny(owner, "/\\\x00") || kind == "" || strings.ContainsAny(kind, "/\\\x00") {
		return "", fmt.Errorf("%w: invalid job artifact identity", ErrInvalidKey)
	}
	return owner + "/artifacts/" + jobID.String() + "/" + kind + ".bin", nil
}

// JobArtifactWriter is an internal capability. It does not change the normal
// FileStorage contract, whose backend-generated keys remain compatibility
// behavior for ordinary file uploads.
type JobArtifactWriter interface {
	PutJobArtifact(ctx context.Context, ownerUID string, jobID domain.ID, kind string, r io.Reader) (string, int64, error)
}
