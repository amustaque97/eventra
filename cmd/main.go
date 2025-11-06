package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/amustaque97/eventra/internal/config"
	"github.com/amustaque97/eventra/internal/leader"
	"github.com/amustaque97/eventra/internal/replication"
	"github.com/amustaque97/eventra/internal/server"
	"github.com/amustaque97/eventra/internal/store"
	"go.uber.org/zap"
)

func main() {
	// Initialize logger
	logger, err := zap.NewProduction()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	logger.Info("starting event store server")

	// Load configuration
	cfg, err := config.LoadConfig()
	if err != nil {
		logger.Fatal("failed to load configuration", zap.Error(err))
	}

	logger.Info("configuration loaded",
		zap.String("node_id", cfg.NodeID),
		zap.String("http_port", cfg.HTTPPort),
		zap.String("data_dir", cfg.DataDir))

	// Initialize event store
	eventStore, err := store.NewEventStore(cfg.DataDir, logger)
	if err != nil {
		logger.Fatal("failed to create event store", zap.Error(err))
	}
	defer eventStore.Close()

	logger.Info("event store initialized",
		zap.Int64("last_applied", eventStore.GetLastApplied()),
		zap.Int64("commit_index", eventStore.GetCommitIndex()))

	// Initialize replicator
	replicator := replication.NewReplicator(cfg.NodeID, eventStore, logger)
	defer replicator.Stop()

	// Initialize HTTP server
	httpServer := server.NewHTTPServer(
		cfg.NodeID,
		cfg.HTTPPort,
		eventStore,
		replicator,
		logger,
	)

	// Start HTTP server in background
	go func() {
		if err := httpServer.Start(); err != nil {
			logger.Error("HTTP server error", zap.Error(err))
		}
	}()

	logger.Info("HTTP server started", zap.String("port", cfg.HTTPPort))

	// Initialize leader election
	nodeAddress := cfg.GetNodeAddress()
	electionMgr, err := leader.NewElectionManager(
		cfg.EtcdEndpoints,
		cfg.NodeID,
		nodeAddress,
		replicator,
		logger,
	)
	if err != nil {
		logger.Fatal("failed to create election manager", zap.Error(err))
	}
	defer electionMgr.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Register node in etcd
	if err := electionMgr.RegisterNode(ctx); err != nil {
		logger.Fatal("failed to register node", zap.Error(err))
	}

	// Start leader election
	if err := electionMgr.Start(ctx); err != nil {
		logger.Fatal("failed to start leader election", zap.Error(err))
	}

	logger.Info("leader election started")

	// Discover and connect to other nodes
	go func() {
		time.Sleep(2 * time.Second) // Wait for other nodes to register

		nodes, err := electionMgr.DiscoverNodes(ctx)
		if err != nil {
			logger.Error("failed to discover nodes", zap.Error(err))
			return
		}

		// Add discovered nodes as followers if we are leader
		for nodeID, address := range nodes {
			replicator.AddFollower(nodeID, address)
		}

		// Watch for node changes
		electionMgr.WatchNodes(ctx)
	}()

	// Wait for interrupt signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	logger.Info("shutting down...")

	// Graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := httpServer.Stop(shutdownCtx); err != nil {
		logger.Error("error stopping HTTP server", zap.Error(err))
	}

	logger.Info("shutdown complete")
}
