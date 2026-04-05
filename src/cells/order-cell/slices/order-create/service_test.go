package ordercreate

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/order-cell/internal/domain"
	"github.com/ghbvf/gocell/cells/order-cell/internal/mem"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// --- test doubles ---

// noopPublisher always succeeds.
type noopPublisher struct{}

func (noopPublisher) Publish(_ context.Context, _ string, _ []byte) error { return nil }

var _ outbox.Publisher = noopPublisher{}

// failPublisher always returns an error.
type failPublisher struct{}

func (failPublisher) Publish(_ context.Context, _ string, _ []byte) error {
	return errors.New("publish unavailable")
}

// recordingPublisher records each publish call.
type recordingPublisher struct {
	calls []publishCall
}

type publishCall struct {
	topic   string
	payload []byte
}

func (p *recordingPublisher) Publish(_ context.Context, topic string, payload []byte) error {
	p.calls = append(p.calls, publishCall{topic: topic, payload: payload})
	return nil
}

func TestService_Create(t *testing.T) {
	tests := []struct {
		name      string
		item      string
		publisher outbox.Publisher
		wantErr   bool
		errCode   errcode.Code
	}{
		{
			name:      "success",
			item:      "widget",
			publisher: noopPublisher{},
		},
		{
			name:      "empty item returns validation error",
			item:      "",
			publisher: noopPublisher{},
			wantErr:   true,
			errCode:   errcode.ErrValidationFailed,
		},
		{
			name:      "publish failure does not fail create",
			item:      "gadget",
			publisher: failPublisher{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := mem.NewOrderRepository()
			svc := NewService(repo, tt.publisher, slog.Default())

			order, err := svc.Create(context.Background(), tt.item)
			if tt.wantErr {
				require.Error(t, err)
				var ecErr *errcode.Error
				require.ErrorAs(t, err, &ecErr)
				assert.Equal(t, tt.errCode, ecErr.Code)
				assert.Nil(t, order)
			} else {
				require.NoError(t, err)
				require.NotNil(t, order)
				assert.Equal(t, tt.item, order.Item)
				assert.Equal(t, "pending", order.Status)
				assert.NotEmpty(t, order.ID)
			}
		})
	}
}

func TestService_Create_PublishesEvent(t *testing.T) {
	repo := mem.NewOrderRepository()
	pub := &recordingPublisher{}
	svc := NewService(repo, pub, slog.Default())

	order, err := svc.Create(context.Background(), "test-item")
	require.NoError(t, err)
	require.NotNil(t, order)

	require.Len(t, pub.calls, 1, "should publish exactly one event")
	assert.Equal(t, TopicOrderCreated, pub.calls[0].topic)
	assert.Contains(t, string(pub.calls[0].payload), order.ID)
}

func TestService_Create_PersistsOrder(t *testing.T) {
	repo := mem.NewOrderRepository()
	svc := NewService(repo, noopPublisher{}, slog.Default())

	order, err := svc.Create(context.Background(), "persisted")
	require.NoError(t, err)

	got, err := repo.GetByID(context.Background(), order.ID)
	require.NoError(t, err)
	assert.Equal(t, order.ID, got.ID)
	assert.Equal(t, "persisted", got.Item)
}

// failRepo is a repository that always fails on Create.
type failRepo struct {
	domain.OrderRepository
}

func (failRepo) Create(_ context.Context, _ *domain.Order) error {
	return errors.New("db connection lost")
}

func TestService_Create_RepoFailure(t *testing.T) {
	svc := NewService(failRepo{}, noopPublisher{}, slog.Default())

	order, err := svc.Create(context.Background(), "item")
	require.Error(t, err)
	assert.Nil(t, order)
	assert.Contains(t, err.Error(), "persist")
}
