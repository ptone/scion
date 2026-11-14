package entadapter

import (
	"context"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/scion/pkg/store"
	"github.com/stretchr/testify/require"
)

func TestInv194SlugReuseAfterSoftDelete(t *testing.T) {
	ctx := context.Background()
	cs, a, pred := finalizeFixture(t)
	now := time.Now()
	none := store.DeletionStateNone
	_, err := cs.FinalizeAgentDeletion(ctx, a.ID, pred, store.DeletionFinalizeSoft,
		store.DeletionFields{State: &none, DeletedAt: &now, ClearLeaseAt: true}, nil)
	require.NoError(t, err)
	b := makeAgent(a.ProjectID, "fin-agent")
	err = cs.CreateAgent(ctx, b)
	t.Logf("create same slug after soft delete: err=%v", err)
}
