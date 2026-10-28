package application

import (
	"context"
	"errors"
	c "github.com/XManCodeing/XMall/internal/modules/telegram/shop/contract"
	"testing"
)

type stampedIdentity struct {
	c.Identity
	stamp string
	uid   uint
}

func (s *stampedIdentity) Resolve(context.Context, c.Actor, bool) (uint, error) { return s.uid, nil }
func (s *stampedIdentity) Generation(context.Context, c.Actor) (string, error)  { return s.stamp, nil }
func TestQueuedSensitiveReplyCannotReviveAfterRebinding(t *testing.T) {
	identity := &stampedIdentity{stamp: "10:7", uid: 7}
	service := New(Dependencies{Identity: identity})
	if err := service.AuthorizeStamp(context.Background(), 1, 12, "10:7"); err != nil {
		t.Fatal(err)
	}
	identity.stamp = "11:7"
	if err := service.AuthorizeStamp(context.Background(), 1, 12, "10:7"); !errors.Is(err, c.ErrDenied) {
		t.Fatal("old binding sensitive reply revived")
	}
	if err := service.AuthorizeStamp(context.Background(), 1, 12, ""); !errors.Is(err, c.ErrDenied) {
		t.Fatal("unstamped legacy message accepted")
	}
}
