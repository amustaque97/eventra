package leader

import (
	"context"
	"fmt"
	"time"

	"github.com/amustaque97/eventra/internal/replication"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"
	"go.uber.org/zap"
)

const (
	leaderElectionPrefix = "/event-store/leader"
	sessionTTL           = 5
)

// ElectionManager handles leader election via etcd
type ElectionManager struct {
	etcdClient *clientv3.Client
	session    *concurrency.Session
	election   *concurrency.Election
	nodeID     string
	nodeAddr   string
	replicator *replication.Replicator
	logger     *zap.Logger
	cancelFunc context.CancelFunc
}

// NewElectionManager creates a new election manager
func NewElectionManager(
	etcdEndpoints []string,
	nodeID string,
	nodeAddr string,
	replicator *replication.Replicator,
	logger *zap.Logger,
) (*ElectionManager, error) {
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   etcdEndpoints,
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create etcd client: %w", err)
	}

	return &ElectionManager{
		etcdClient: client,
		nodeID:     nodeID,
		nodeAddr:   nodeAddr,
		replicator: replicator,
		logger:     logger,
	}, nil
}

// Start starts the leader election process
func (e *ElectionManager) Start(ctx context.Context) error {
	// Create a session with TTL
	session, err := concurrency.NewSession(e.etcdClient, concurrency.WithTTL(sessionTTL))
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	e.session = session

	// Create an election
	e.election = concurrency.NewElection(session, leaderElectionPrefix)

	// Campaign for leadership in a separate goroutine
	campaignCtx, cancel := context.WithCancel(ctx)
	e.cancelFunc = cancel

	go e.campaign(campaignCtx)
	go e.observe(campaignCtx)

	return nil
}

// campaign attempts to become the leader
func (e *ElectionManager) campaign(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			e.logger.Info("campaigning for leadership")

			// Try to become leader
			if err := e.election.Campaign(ctx, e.nodeID); err != nil {
				if err == context.Canceled {
					return
				}
				e.logger.Error("campaign failed", zap.Error(err))
				time.Sleep(1 * time.Second)
				continue
			}

			// Successfully became leader
			e.logger.Info("elected as leader", zap.String("node_id", e.nodeID))

			// Get current term from etcd revision
			resp, err := e.election.Leader(ctx)
			if err != nil {
				e.logger.Error("failed to get leader info", zap.Error(err))
				continue
			}

			term := resp.Kvs[0].CreateRevision

			// Notify replicator
			e.replicator.BecomeLeader(term)

			// Register as leader in etcd
			if err := e.registerAsLeader(ctx); err != nil {
				e.logger.Error("failed to register as leader", zap.Error(err))
			}

			// Wait until leadership is lost
			select {
			case <-e.session.Done():
				e.logger.Warn("session expired, lost leadership")
				e.replicator.BecomeFollower("", "", term)
			case <-ctx.Done():
				return
			}
		}
	}
}

// observe watches for leader changes
func (e *ElectionManager) observe(ctx context.Context) {
	observeCh := e.election.Observe(ctx)

	for {
		select {
		case resp := <-observeCh:
			if len(resp.Kvs) == 0 {
				continue
			}

			leaderID := string(resp.Kvs[0].Value)
			term := resp.Kvs[0].CreateRevision

			if leaderID == e.nodeID {
				// We are the leader
				continue
			}

			// Get leader address from etcd
			leaderAddr, err := e.getNodeAddress(ctx, leaderID)
			if err != nil {
				e.logger.Error("failed to get leader address",
					zap.String("leader_id", leaderID),
					zap.Error(err))
				continue
			}

			e.logger.Info("new leader detected",
				zap.String("leader_id", leaderID),
				zap.String("leader_addr", leaderAddr),
				zap.Int64("term", term))

			// Notify replicator
			e.replicator.BecomeFollower(leaderID, leaderAddr, term)

		case <-ctx.Done():
			return
		}
	}
}

// registerAsLeader registers this node as the leader in etcd
func (e *ElectionManager) registerAsLeader(ctx context.Context) error {
	key := fmt.Sprintf("/event-store/nodes/%s", e.nodeID)
	value := e.nodeAddr

	_, err := e.etcdClient.Put(ctx, key, value)
	if err != nil {
		return fmt.Errorf("failed to register node: %w", err)
	}

	return nil
}

// RegisterNode registers a node in etcd
func (e *ElectionManager) RegisterNode(ctx context.Context) error {
	key := fmt.Sprintf("/event-store/nodes/%s", e.nodeID)
	value := e.nodeAddr

	// Put with lease to auto-expire if node dies
	lease, err := e.etcdClient.Grant(ctx, sessionTTL*2)
	if err != nil {
		return fmt.Errorf("failed to create lease: %w", err)
	}

	_, err = e.etcdClient.Put(ctx, key, value, clientv3.WithLease(lease.ID))
	if err != nil {
		return fmt.Errorf("failed to register node: %w", err)
	}

	// Keep lease alive
	keepAliveCh, err := e.etcdClient.KeepAlive(ctx, lease.ID)
	if err != nil {
		return fmt.Errorf("failed to keep lease alive: %w", err)
	}

	// Consume keep-alive responses
	go func() {
		for range keepAliveCh {
			// Lease renewed
		}
	}()

	e.logger.Info("registered node in etcd",
		zap.String("node_id", e.nodeID),
		zap.String("address", e.nodeAddr))

	return nil
}

// DiscoverNodes discovers all nodes in the cluster from etcd
func (e *ElectionManager) DiscoverNodes(ctx context.Context) (map[string]string, error) {
	resp, err := e.etcdClient.Get(ctx, "/event-store/nodes/", clientv3.WithPrefix())
	if err != nil {
		return nil, fmt.Errorf("failed to discover nodes: %w", err)
	}

	nodes := make(map[string]string)
	for _, kv := range resp.Kvs {
		// Extract node ID from key
		key := string(kv.Key)
		nodeID := key[len("/event-store/nodes/"):]
		address := string(kv.Value)

		if nodeID != e.nodeID {
			nodes[nodeID] = address
		}
	}

	e.logger.Info("discovered nodes", zap.Int("count", len(nodes)))

	return nodes, nil
}

// getNodeAddress retrieves a node's address from etcd
func (e *ElectionManager) getNodeAddress(ctx context.Context, nodeID string) (string, error) {
	key := fmt.Sprintf("/event-store/nodes/%s", nodeID)
	resp, err := e.etcdClient.Get(ctx, key)
	if err != nil {
		return "", fmt.Errorf("failed to get node address: %w", err)
	}

	if len(resp.Kvs) == 0 {
		return "", fmt.Errorf("node not found: %s", nodeID)
	}

	return string(resp.Kvs[0].Value), nil
}

// WatchNodes watches for node changes in the cluster
func (e *ElectionManager) WatchNodes(ctx context.Context) {
	watchCh := e.etcdClient.Watch(ctx, "/event-store/nodes/", clientv3.WithPrefix())

	for {
		select {
		case watchResp := <-watchCh:
			for _, event := range watchResp.Events {
				key := string(event.Kv.Key)
				nodeID := key[len("/event-store/nodes/"):]

				if nodeID == e.nodeID {
					continue
				}

				switch event.Type {
				case clientv3.EventTypePut:
					address := string(event.Kv.Value)
					e.logger.Info("node joined",
						zap.String("node_id", nodeID),
						zap.String("address", address))

					// Add follower if we are leader
					if e.replicator.IsLeader() {
						e.replicator.AddFollower(nodeID, address)
					}

				case clientv3.EventTypeDelete:
					e.logger.Info("node left", zap.String("node_id", nodeID))

					// Remove follower if we are leader
					if e.replicator.IsLeader() {
						e.replicator.RemoveFollower(nodeID)
					}
				}
			}

		case <-ctx.Done():
			return
		}
	}
}

// Stop stops the election manager
func (e *ElectionManager) Stop() error {
	if e.cancelFunc != nil {
		e.cancelFunc()
	}

	if e.session != nil {
		if err := e.session.Close(); err != nil {
			return err
		}
	}

	if e.etcdClient != nil {
		return e.etcdClient.Close()
	}

	return nil
}
