package comment_test

import (
	"testing"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/idgen"

	"github.com/aero/aero-id/internal/comment"
)

func TestValidateNewComment_topLevel(t *testing.T) {
	err := comment.ValidateNewComment(nil)
	if err != nil {
		t.Fatalf("top-level comment should be allowed: %v", err)
	}
}

func TestValidateNewComment_replyToTopLevel(t *testing.T) {
	root := idgen.NewID()
	parent := &comment.Comment{ID: root, ParentID: nil, RootID: root}
	err := comment.ValidateNewComment(parent)
	if err != nil {
		t.Fatalf("reply to top-level should be allowed: %v", err)
	}
}

func TestValidateNewComment_replyToReplyForbidden(t *testing.T) {
	root := idgen.NewID()
	parent := &comment.Comment{ID: idgen.NewID(), ParentID: &root, RootID: root}
	err := comment.ValidateNewComment(parent)
	if err == nil {
		t.Fatal("expected error for depth >= 3")
	}
}

func TestNewTopLevelComment_setsRootToSelf(t *testing.T) {
	c := comment.NewComment(idgen.NewID(), "uid-x", domain.TargetTypeFile, idgen.NewID(), nil, "hi")
	if c.ParentID != nil {
		t.Fatal("ParentID should be nil")
	}
	if c.RootID != c.ID {
		t.Fatal("RootID should equal ID for top-level")
	}
}

func TestNewReply_inheritsRootID(t *testing.T) {
	root := idgen.NewID()
	parent := &comment.Comment{ID: root, ParentID: nil, RootID: root}
	c := comment.NewComment(idgen.NewID(), "uid-y", domain.TargetTypeFile, idgen.NewID(), parent, "re: hi")
	if c.RootID != root {
		t.Fatalf("RootID = %s, want %s", c.RootID, root)
	}
	if c.ParentID == nil || *c.ParentID != parent.ID {
		t.Fatal("ParentID should equal parent.ID")
	}
}
