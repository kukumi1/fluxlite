package service

import (
	"testing"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
)

func TestCollectionUsage(t *testing.T) {
	soon := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	later := soon.AddDate(0, 1, 0)
	capped := func(quota, in, out int64, expires time.Time) *model.SingBoxUser {
		return &model.SingBoxUser{BaseQuotaBytes: quota, UsedIn: in, UsedOut: out, ExpiresAt: expires}
	}

	t.Run("sums traffic and allowance, expires with the last node", func(t *testing.T) {
		got := collectionUsage([]*model.SingBoxUser{capped(100, 1, 10, soon), capped(200, 2, 20, later)})
		if got.Upload != 3 || got.Download != 30 || got.Total != 300 || got.Expire == nil || !got.Expire.Equal(later) {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("one unlimited permanent node lifts both limits", func(t *testing.T) {
		got := collectionUsage([]*model.SingBoxUser{capped(100, 1, 10, soon), capped(0, 2, 20, time.Time{})})
		if got.Upload != 3 || got.Download != 30 || got.Total != 0 || got.Expire != nil {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("top-ups count toward the allowance", func(t *testing.T) {
		u := capped(100, 0, 0, soon)
		u.TopUpBytes = 50
		if got := collectionUsage([]*model.SingBoxUser{u}); got.Total != 150 {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("no nodes reports nothing", func(t *testing.T) {
		if got := collectionUsage(nil); got.Total != 0 || got.Expire != nil {
			t.Fatalf("%+v", got)
		}
	})
}
