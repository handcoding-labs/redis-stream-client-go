package impl

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/redis/go-redis/v9"

	"github.com/handcoding-labs/redis-stream-client-go/types/errs"
)

// nodeReport is what a single cluster node says about the cluster, as returned by CLUSTER INFO and
// CLUSTER NODES. err is set when the node could not be asked.
type nodeReport struct {
	addr  string
	state string // cluster_state from CLUSTER INFO
	slots string // slot ownership as seen by this node, see slotOwnership
	err   error
}

// ensureClusterSettled is the terminal check behind ReinitTopology: it asks every known node for its
// view and reports errs.ErrClusterNotSettled unless the cluster is settled. It only reads.
func (r *RecoverableRedisStreamClient) ensureClusterSettled(ctx context.Context, cluster *redis.ClusterClient) error {
	var (
		mu      sync.Mutex
		reports []nodeReport
	)

	// ForEachShard runs the callback on every known node (masters and replicas) concurrently. A node
	// that cannot be asked is recorded rather than returned, so one dead node does not stop the rest
	// from being asked.
	err := cluster.ForEachShard(ctx, func(ctx context.Context, node *redis.Client) error {
		rep := nodeReport{addr: node.Options().Addr}
		info, ierr := node.ClusterInfo(ctx).Result()
		if ierr != nil {
			rep.err = ierr
		} else {
			rep.state = clusterInfoField(info, "cluster_state")
			nodes, nerr := node.ClusterNodes(ctx).Result()
			if nerr != nil {
				rep.err = nerr
			} else {
				rep.slots = slotOwnership(nodes)
			}
		}
		mu.Lock()
		reports = append(reports, rep)
		mu.Unlock()
		return nil
	})
	if err != nil {
		return errs.NewRedisError(errs.OpReinitTopology, err)
	}

	return evaluateSettled(reports)
}

// evaluateSettled decides from the nodes' reports whether the cluster is settled. It is settled when,
// among the nodes that answered, every one reports cluster_state:ok and they all report the same slot
// ownership. This is deliberately the same notion of "settled" that `redis-cli --cluster check` uses
// for "all nodes agree about slots configuration", plus the cluster_state that CLUSTER INFO documents.
//
// Nodes that cannot be reached are left out: a dead node (for example the old master after an
// automatic failover) can neither disagree nor be asked, and cluster_state on the live nodes already
// says whether every slot is still served. If no node answers at all that is a connectivity problem,
// not an unsettled cluster, and is reported as such.
func evaluateSettled(reports []nodeReport) error {
	sort.Slice(reports, func(i, j int) bool { return reports[i].addr < reports[j].addr })

	var answered []nodeReport
	var firstErr error
	for _, rep := range reports {
		if rep.err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", rep.addr, rep.err)
			}
			continue
		}
		answered = append(answered, rep)
	}
	if len(answered) == 0 {
		if firstErr == nil {
			firstErr = fmt.Errorf("no cluster nodes known")
		}
		return errs.NewRedisError(errs.OpReinitTopology, firstErr)
	}

	for _, rep := range answered {
		if rep.state != "ok" {
			return fmt.Errorf("%w: node %s reports cluster_state:%s", errs.ErrClusterNotSettled, rep.addr, rep.state)
		}
	}

	ref := answered[0]
	for _, rep := range answered[1:] {
		if rep.slots != ref.slots {
			return fmt.Errorf("%w: nodes %s and %s disagree about which node owns which slots",
				errs.ErrClusterNotSettled, ref.addr, rep.addr)
		}
	}
	return nil
}

// clusterInfoField returns the value of a "key:value" line in CLUSTER INFO output, or "" if absent.
func clusterInfoField(info, key string) string {
	for _, line := range strings.Split(info, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), key+":"); ok {
			return v
		}
	}
	return ""
}

// slotOwnership reduces CLUSTER NODES output to who owns which slots, as one node sees it: for every
// node that owns slots, its id and slot ranges. Everything else in the output (roles of replicas,
// ping times, epochs, link state) is deliberately ignored, as are the bracketed migrating/importing
// markers, which appear only on the line of the node that is being asked.
func slotOwnership(clusterNodes string) string {
	var owners []string
	for _, line := range strings.Split(clusterNodes, "\n") {
		f := strings.Fields(line)
		// <id> <ip:port@cport> <flags> <master> <ping-sent> <pong-recv> <config-epoch> <link-state> <slot>...
		if len(f) < 9 {
			continue
		}
		var slots []string
		for _, s := range f[8:] {
			if !strings.HasPrefix(s, "[") {
				slots = append(slots, s)
			}
		}
		if len(slots) > 0 {
			owners = append(owners, f[0]+" "+strings.Join(slots, ","))
		}
	}
	sort.Strings(owners)
	return strings.Join(owners, "\n")
}
