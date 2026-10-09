package impl

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/handcoding-labs/redis-stream-client-go/types/errs"
)

// Output of CLUSTER NODES as seen by 7005 (the newly promoted master of slots 0-5460), with 7000 as
// its replica and the other two shards unchanged.
const nodesAfterFailoverSeenByNewMaster = `8131150225bcb3f472adec61a8cdde80e5762ee7 127.0.0.1:7004@17004 slave b20e5debd87633b438bf9abc08d935492e951a09 0 1791318516010 83 connected
b4a7d4fcabc80b935770ba0d58df58e803184261 127.0.0.1:7003@17003 slave 233620969e48c9460e7563ece0fb78a3b02a9bcb 0 1791318515099 91 connected
039e49de593c754c2b166f27ec1c80b5210bbd58 127.0.0.1:7005@17005 myself,master - 0 1791318515000 93 connected 0-5460
233620969e48c9460e7563ece0fb78a3b02a9bcb 127.0.0.1:7001@17001 master - 0 1791318515503 91 connected 5461-10922
df9e4cf8f3f005190417f49ce10cd2cfd484f66f 127.0.0.1:7000@17000 slave 039e49de593c754c2b166f27ec1c80b5210bbd58 0 1791318516109 93 connected
b20e5debd87633b438bf9abc08d935492e951a09 127.0.0.1:7002@17002 master - 0 1791318516109 83 connected 10923-16383
`

// The same cluster as seen by 7000, the demoted old master, which has learned the new topology; only
// the per-observer details differ (myself marker, ping times, line order).
const nodesAfterFailoverSeenByReplica = `b20e5debd87633b438bf9abc08d935492e951a09 127.0.0.1:7002@17002 master - 0 1791318516038 83 connected 10923-16383
df9e4cf8f3f005190417f49ce10cd2cfd484f66f 127.0.0.1:7000@17000 myself,slave 039e49de593c754c2b166f27ec1c80b5210bbd58 0 1791318514000 93 connected
039e49de593c754c2b166f27ec1c80b5210bbd58 127.0.0.1:7005@17005 master - 0 1791318515532 93 connected 0-5460
8131150225bcb3f472adec61a8cdde80e5762ee7 127.0.0.1:7004@17004 slave b20e5debd87633b438bf9abc08d935492e951a09 0 1791318516000 83 connected
b4a7d4fcabc80b935770ba0d58df58e803184261 127.0.0.1:7003@17003 slave 233620969e48c9460e7563ece0fb78a3b02a9bcb 0 1791318515000 91 connected
233620969e48c9460e7563ece0fb78a3b02a9bcb 127.0.0.1:7001@17001 master - 0 1791318515432 91 connected 5461-10922
`

// A node that has not yet heard about the failover and still thinks 7000 owns slots 0-5460.
const nodesBeforeFailoverStaleView = `8131150225bcb3f472adec61a8cdde80e5762ee7 127.0.0.1:7004@17004 slave b20e5debd87633b438bf9abc08d935492e951a09 0 1791318516010 83 connected
b4a7d4fcabc80b935770ba0d58df58e803184261 127.0.0.1:7003@17003 myself,slave 233620969e48c9460e7563ece0fb78a3b02a9bcb 0 1791318515099 91 connected
039e49de593c754c2b166f27ec1c80b5210bbd58 127.0.0.1:7005@17005 slave df9e4cf8f3f005190417f49ce10cd2cfd484f66f 0 1791318515000 92 connected
233620969e48c9460e7563ece0fb78a3b02a9bcb 127.0.0.1:7001@17001 master - 0 1791318515503 91 connected 5461-10922
df9e4cf8f3f005190417f49ce10cd2cfd484f66f 127.0.0.1:7000@17000 master - 0 1791318516109 92 connected 0-5460
b20e5debd87633b438bf9abc08d935492e951a09 127.0.0.1:7002@17002 master - 0 1791318516109 83 connected 10923-16383
`

func TestSlotOwnershipIgnoresEverythingButWhoOwnsWhichSlots(t *testing.T) {
	// two observers, different line order, myself marker, ping times and replica details
	require.Equal(t, slotOwnership(nodesAfterFailoverSeenByNewMaster), slotOwnership(nodesAfterFailoverSeenByReplica))

	// config epochs and replica roles are not part of the answer, so they cannot cause a disagreement
	differentEpochs := strings.ReplaceAll(nodesAfterFailoverSeenByReplica, " 93 connected", " 94 connected")
	require.Equal(t, slotOwnership(nodesAfterFailoverSeenByReplica), slotOwnership(differentEpochs))

	// open (migrating/importing) slots only appear on the asked node's own line and are ignored
	withOpenSlot := strings.Replace(nodesAfterFailoverSeenByNewMaster,
		"myself,master - 0 1791318515000 93 connected 0-5460",
		"myself,master - 0 1791318515000 93 connected 0-5460 [93->-233620969e48c9460e7563ece0fb78a3b02a9bcb]", 1)
	require.Equal(t, slotOwnership(nodesAfterFailoverSeenByNewMaster), slotOwnership(withOpenSlot))

	// a different owner for a slot range is a different answer
	require.NotEqual(t, slotOwnership(nodesAfterFailoverSeenByReplica), slotOwnership(nodesBeforeFailoverStaleView))
}

func TestEvaluateSettled(t *testing.T) {
	healthy := func(addr, nodes string) nodeReport {
		return nodeReport{addr: addr, state: "ok", slots: slotOwnership(nodes)}
	}
	down := func(addr string) nodeReport {
		return nodeReport{addr: addr, err: errors.New("dial tcp: connection refused")}
	}

	tests := []struct {
		name           string
		reports        []nodeReport
		wantErr        bool
		wantNotSettled bool // the error must wrap errs.ErrClusterNotSettled
		contains       string
	}{
		{
			name: "all nodes ok and agree",
			reports: []nodeReport{
				healthy("127.0.0.1:7005", nodesAfterFailoverSeenByNewMaster),
				healthy("127.0.0.1:7000", nodesAfterFailoverSeenByReplica),
			},
		},
		{
			name: "a node still holds the pre-failover view",
			reports: []nodeReport{
				healthy("127.0.0.1:7005", nodesAfterFailoverSeenByNewMaster),
				healthy("127.0.0.1:7003", nodesBeforeFailoverStaleView),
			},
			wantErr: true, wantNotSettled: true, contains: "disagree",
		},
		{
			name: "a node reports cluster_state fail",
			reports: []nodeReport{
				healthy("127.0.0.1:7005", nodesAfterFailoverSeenByNewMaster),
				{addr: "127.0.0.1:7001", state: "fail", slots: slotOwnership(nodesAfterFailoverSeenByNewMaster)},
			},
			wantErr: true, wantNotSettled: true, contains: "127.0.0.1:7001",
		},
		{
			name: "an unreachable node is left out, the rest agree",
			reports: []nodeReport{
				healthy("127.0.0.1:7005", nodesAfterFailoverSeenByNewMaster),
				healthy("127.0.0.1:7000", nodesAfterFailoverSeenByReplica),
				down("127.0.0.1:7002"),
			},
		},
		{
			name: "an unreachable node does not hide a disagreement among the others",
			reports: []nodeReport{
				healthy("127.0.0.1:7005", nodesAfterFailoverSeenByNewMaster),
				healthy("127.0.0.1:7003", nodesBeforeFailoverStaleView),
				down("127.0.0.1:7002"),
			},
			wantErr: true, wantNotSettled: true,
		},
		{
			name:    "no node answers is a connectivity error, not an unsettled cluster",
			reports: []nodeReport{down("127.0.0.1:7000"), down("127.0.0.1:7001")},
			wantErr: true, wantNotSettled: false, contains: "connection refused",
		},
		{
			name:    "no nodes known",
			wantErr: true, wantNotSettled: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := evaluateSettled(tc.reports)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Equal(t, tc.wantNotSettled, errors.Is(err, errs.ErrClusterNotSettled), "err = %v", err)
			if tc.contains != "" {
				require.Contains(t, err.Error(), tc.contains)
			}
		})
	}
}

func TestClusterInfoField(t *testing.T) {
	info := "cluster_state:ok\r\ncluster_slots_assigned:16384\r\ncluster_current_epoch:93\r\n"
	require.Equal(t, "ok", clusterInfoField(info, "cluster_state"))
	require.Equal(t, "93", clusterInfoField(info, "cluster_current_epoch"))
	require.Equal(t, "", clusterInfoField(info, "missing"))
}
