package dispatch

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	kwh "github.com/ghbvf/gocell/kernel/webhook"
	"github.com/ghbvf/gocell/pkg/errcode"
)

const dispatchTS = 1700000000

// testStore returns a SourceRegistry seeded with one source under sourceID.
func testStore(t *testing.T, sourceID string) kwh.SourceStore {
	t.Helper()
	reg := kwh.NewSourceRegistry()
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i + 1)
	}
	sid, err := kwh.NewSourceID(sourceID)
	require.NoError(t, err)
	src, err := kwh.NewSource(sid, secret)
	require.NoError(t, err)
	require.NoError(t, reg.Register(src))
	return reg
}

func okSelector(_ context.Context, _ []byte) (string, error) {
	return "https://hooks.example.test/", nil
}

func dispatchReq(contractID, sourceID, cellID string) cell.WebhookDispatchRequest {
	return cell.WebhookDispatchRequest{
		Spec:     kwh.DispatchSpec{ContractID: contractID, SourceID: sourceID, CellID: cellID},
		Selector: okSelector,
	}
}

func testClock() *clockmock.FakeClock { return clockmock.New(time.Unix(dispatchTS, 0)) }

func TestBuildConsumers_HappyPath(t *testing.T) {
	t.Parallel()
	store := testStore(t, "shopify")
	reqs := []cell.WebhookDispatchRequest{
		dispatchReq("webhook.shopify.orders.v1", "shopify", "ordercore"),
	}
	consumers, err := BuildConsumers(testClock(), reqs, store, kwh.NewSafePolicy(), kwh.Metrics{})
	require.NoError(t, err)
	require.Len(t, consumers, 1)

	c := consumers[0]
	assert.Equal(t, "webhook.shopify.orders.v1", c.Spec.ID)
	assert.Equal(t, cellvocab.ContractEvent, c.Spec.Kind, "registered as event-kind so AddContractHandler accepts it")
	assert.Equal(t, "webhook.shopify.orders.v1", c.Spec.Topic, "Topic == contract ID")
	assert.NotEmpty(t, c.Spec.Transport)
	assert.Equal(t, "ordercore", c.ConsumerGroup)
	assert.Equal(t, "ordercore", c.CellID)
	require.NotNil(t, c.Handler)
	// Synthesized spec must pass the contractspec event validator (what
	// eventrouter.AddContractHandler enforces).
	require.NoError(t, c.Spec.Validate())
}

func TestBuildConsumers_Empty(t *testing.T) {
	t.Parallel()
	consumers, err := BuildConsumers(testClock(), nil, testStore(t, "s"), kwh.NewSafePolicy(), kwh.Metrics{})
	require.NoError(t, err)
	assert.Empty(t, consumers)
}

func TestBuildConsumers_NilStore(t *testing.T) {
	t.Parallel()
	_, err := BuildConsumers(testClock(), nil, nil, kwh.NewSafePolicy(), kwh.Metrics{})
	requireWebhookConfigErr(t, err)
}

func TestBuildConsumers_NilPolicy(t *testing.T) {
	t.Parallel()
	_, err := BuildConsumers(testClock(), nil, testStore(t, "s"), nil, kwh.Metrics{})
	requireWebhookConfigErr(t, err)
}

func TestBuildConsumers_UnregisteredSource(t *testing.T) {
	t.Parallel()
	store := testStore(t, "shopify")
	reqs := []cell.WebhookDispatchRequest{dispatchReq("webhook.x.v1", "stripe", "c")} // "stripe" not registered
	_, err := BuildConsumers(testClock(), reqs, store, kwh.NewSafePolicy(), kwh.Metrics{})
	requireWebhookConfigErr(t, err)
}

func TestBuildConsumers_NilSelector(t *testing.T) {
	t.Parallel()
	store := testStore(t, "shopify")
	req := cell.WebhookDispatchRequest{
		Spec:     kwh.DispatchSpec{ContractID: "webhook.x.v1", SourceID: "shopify", CellID: "c"},
		Selector: nil,
	}
	_, err := BuildConsumers(testClock(), []cell.WebhookDispatchRequest{req}, store, kwh.NewSafePolicy(), kwh.Metrics{})
	requireWebhookConfigErr(t, err)
}

func TestBuildConsumers_InvalidSpec(t *testing.T) {
	t.Parallel()
	store := testStore(t, "shopify")
	req := dispatchReq("", "shopify", "c") // empty ContractID → spec invalid
	_, err := BuildConsumers(testClock(), []cell.WebhookDispatchRequest{req}, store, kwh.NewSafePolicy(), kwh.Metrics{})
	requireWebhookConfigErr(t, err)
}

func requireWebhookConfigErr(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var ee *errcode.Error
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, errcode.ErrWebhookConfigInvalid, ee.Code)
}
