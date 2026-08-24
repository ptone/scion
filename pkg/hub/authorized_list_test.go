package hub

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type authorizedListTestItem struct{ id string }

func TestAuthorizedListRejectsCandidateCap(t *testing.T) {
	items := make([]authorizedListTestItem, authorizedListMaxCandidates+1)
	for i := range items {
		items[i] = authorizedListTestItem{id: fmt.Sprint(i)}
	}
	_, err := authorizedList(context.Background(), nil, "", 1,
		func(_ context.Context, cursor string, limit int) (authorizedCandidatePage[authorizedListTestItem], error) {
			start := 0
			if cursor != "" {
				_, _ = fmt.Sscan(cursor, &start)
				start++
			}
			end := start + limit
			if end > len(items) {
				end = len(items)
			}
			next := ""
			if end < len(items) {
				next = items[end-1].id
			}
			return authorizedCandidatePage[authorizedListTestItem]{Items: items[start:end], NextCursor: next}, nil
		}, func(*authorizedListTestItem) Resource { return Resource{} }, func(item *authorizedListTestItem) string { return item.id },
		func(_ context.Context, _ Identity, resources []Resource) ([]bool, error) {
			return makeAllowed(len(resources)), nil
		})
	require.ErrorIs(t, err, ErrAuthorizedListIncomplete)
}

func TestAuthorizedListReturnsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	_, err := authorizedList(ctx, nil, "", 1,
		func(context.Context, string, int) (authorizedCandidatePage[authorizedListTestItem], error) {
			called = true
			return authorizedCandidatePage[authorizedListTestItem]{}, nil
		},
		func(*authorizedListTestItem) Resource { return Resource{} }, func(*authorizedListTestItem) string { return "" },
		func(context.Context, Identity, []Resource) ([]bool, error) { return nil, errors.New("unexpected") })
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, called)
}
