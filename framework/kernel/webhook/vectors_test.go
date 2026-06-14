package webhook

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
)

// vector is one HMAC test vector from testdata/webhook-hmac-vectors.yaml.
type vector struct {
	Name         string `yaml:"name"`
	SourceID     string `yaml:"sourceId"`
	SecretBase64 string `yaml:"secretBase64"`
	DeliveryID   string `yaml:"deliveryId"`
	Timestamp    string `yaml:"timestamp"`
	Payload      string `yaml:"payload"`
	Signature    string `yaml:"signature"`
	Valid        bool   `yaml:"valid"`
}

func (v vector) secretBytes(t *testing.T) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(v.SecretBase64)
	require.NoError(t, err, "vector %s: bad secretBase64", v.Name)
	return raw
}

// unixTime returns the vector timestamp as a time.Time.
func (v vector) unixTime(t *testing.T) time.Time {
	t.Helper()
	sec, err := strconv.ParseInt(v.Timestamp, 10, 64)
	require.NoError(t, err, "vector %s: non-numeric timestamp", v.Name)
	return time.Unix(sec, 0)
}

func loadVectors(t *testing.T) []vector {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "webhook-hmac-vectors.yaml"))
	require.NoError(t, err)
	var doc struct {
		Vectors []vector `yaml:"vectors"`
	}
	require.NoError(t, yaml.Unmarshal(data, &doc))
	require.NotEmpty(t, doc.Vectors)
	return doc.Vectors
}

func loadVector(t *testing.T, name string) vector {
	t.Helper()
	for _, v := range loadVectors(t) {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("vector %q not found", name)
	return vector{}
}

// clockmockAt returns a clock pinned to ts (skew zero against a vector signed
// at ts).
func clockmockAt(ts time.Time) clock.Clock { return clockmock.New(ts) }
