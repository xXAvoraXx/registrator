package main

import (
	"context"
	"testing"
	"time"

	testassert "github.com/stretchr/testify/assert"
)

func TestHashIndexDeterministic(t *testing.T) {
	a := hashIndex("service-1", 5)
	b := hashIndex("service-1", 5)
	testassert.Equal(t, a, b)
	testassert.True(t, a >= 0 && a < 5)
}

func TestDeterministicOwnerNode(t *testing.T) {
	nodes := []string{"node-c", "node-a", "node-b"}
	ownerA := deterministicOwnerNode("svc-1", nodes)
	ownerB := deterministicOwnerNode("svc-1", []string{"node-b", "node-c", "node-a"})
	testassert.Equal(t, ownerA, ownerB)
	testassert.Contains(t, []string{"node-a", "node-b", "node-c"}, ownerA)
}

func TestMemoryLockStoreTryLock(t *testing.T) {
	store := newMemoryLockStore()
	ok, err := store.TryLock(context.Background(), "k", "node-a", 2*time.Second)
	testassert.NoError(t, err)
	testassert.True(t, ok)

	ok, err = store.TryLock(context.Background(), "k", "node-b", 2*time.Second)
	testassert.NoError(t, err)
	testassert.False(t, ok)
}

func TestMemoryLockStoreState(t *testing.T) {
	store := newMemoryLockStore()
	err := store.Set(context.Background(), "state-k", "hash-v", 2*time.Second)
	testassert.NoError(t, err)
	v, err := store.Get(context.Background(), "state-k")
	testassert.NoError(t, err)
	testassert.Equal(t, "hash-v", v)
}
