package testutils_test

import (
	"testing"

	"github.com/maikdotfi/agentgarage/metaharness/agentdb"
	"github.com/maikdotfi/agentgarage/metaharness/testutils"
)

func TestMemKV(t *testing.T) {
	testutils.RunKVSuite(t, func(t *testing.T) agentdb.KV {
		t.Helper()
		return &testutils.MemKV{}
	})
}
