package main

import (
	"bufio"
	"context"
	"fmt"
	"hash/fnv"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cenkalti/backoff"
	swarmapi "github.com/docker/docker/api/types/swarm"
	dockerapi "github.com/fsouza/go-dockerclient"
	"github.com/gliderlabs/registrator/bridge"
	"github.com/sirupsen/logrus"
)

const (
	clusterIDPrefixLen     = 8
	lockRetryMaxElapsed    = 5 * time.Second
	managerRetryMaxElapsed = 5 * time.Second
	defaultDockerAPIVer    = "1.41"
	redisDialTimeout       = 2 * time.Second
	redisOpTimeout         = 3 * time.Second
)

type lockStore interface {
	TryLock(ctx context.Context, key, value string, ttl time.Duration) (bool, error)
	Unlock(ctx context.Context, key, value string) error
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}

type memoryLockStore struct {
	mu      sync.Mutex
	locks   map[string]lockEntry
	records map[string]lockEntry
}

type lockEntry struct {
	value     string
	expiresAt time.Time
}

func newMemoryLockStore() *memoryLockStore {
	return &memoryLockStore{
		locks:   make(map[string]lockEntry),
		records: make(map[string]lockEntry),
	}
}

func (m *memoryLockStore) TryLock(_ context.Context, key, value string, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if cur, ok := m.locks[key]; ok && now.Before(cur.expiresAt) && cur.value != value {
		return false, nil
	}
	m.locks[key] = lockEntry{value: value, expiresAt: now.Add(ttl)}
	return true, nil
}

func (m *memoryLockStore) Unlock(_ context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.locks[key]; ok && cur.value == value {
		delete(m.locks, key)
	}
	return nil
}

func (m *memoryLockStore) Get(_ context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if cur, ok := m.records[key]; ok && now.Before(cur.expiresAt) {
		return cur.value, nil
	}
	delete(m.records, key)
	return "", nil
}

func (m *memoryLockStore) Set(_ context.Context, key, value string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[key] = lockEntry{value: value, expiresAt: time.Now().Add(ttl)}
	return nil
}

func (m *memoryLockStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.records, key)
	return nil
}

type redisLockStore struct {
	addr string
}

func newRedisLockStore(addr string) *redisLockStore {
	return &redisLockStore{addr: addr}
}

func (r *redisLockStore) TryLock(ctx context.Context, key, value string, ttl time.Duration) (bool, error) {
	reply, err := r.cmd(ctx, "SET", key, value, "NX", "EX", fmt.Sprintf("%d", int(ttl.Seconds())))
	if err != nil {
		return false, err
	}
	return reply == "OK", nil
}

func (r *redisLockStore) Unlock(ctx context.Context, key, value string) error {
	current, err := r.Get(ctx, key)
	if err != nil {
		return err
	}
	if current != value {
		return nil
	}
	_, err = r.cmd(ctx, "DEL", key)
	return err
}

func (r *redisLockStore) Get(ctx context.Context, key string) (string, error) {
	return r.cmd(ctx, "GET", key)
}

func (r *redisLockStore) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	_, err := r.cmd(ctx, "SET", key, value, "EX", fmt.Sprintf("%d", int(ttl.Seconds())))
	return err
}

func (r *redisLockStore) Delete(ctx context.Context, key string) error {
	_, err := r.cmd(ctx, "DEL", key)
	return err
}

func (r *redisLockStore) cmd(ctx context.Context, args ...string) (string, error) {
	dialer := net.Dialer{Timeout: redisDialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", r.addr)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	deadline := time.Now().Add(redisOpTimeout)
	_ = conn.SetDeadline(deadline)

	var b strings.Builder
	b.WriteString(fmt.Sprintf("*%d\r\n", len(args)))
	for _, arg := range args {
		b.WriteString(fmt.Sprintf("$%d\r\n%s\r\n", len(arg), arg))
	}
	if _, err := conn.Write([]byte(b.String())); err != nil {
		return "", err
	}

	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", nil
	}
	switch line[0] {
	case '+':
		return strings.TrimPrefix(line, "+"), nil
	case '$':
		if line == "$-1" {
			return "", nil
		}
		var length int
		if _, err := fmt.Sscanf(line, "$%d", &length); err != nil {
			return "", err
		}
		payload := make([]byte, length+2)
		if _, err := reader.Read(payload); err != nil {
			return "", err
		}
		return string(payload[:length]), nil
	case ':':
		return strings.TrimPrefix(line, ":"), nil
	case '-':
		return "", fmt.Errorf("redis error: %s", strings.TrimPrefix(line, "-"))
	default:
		return "", fmt.Errorf("unexpected redis response: %q", line)
	}
}

type fallbackLockStore struct {
	primary   lockStore
	secondary lockStore
}

func (f *fallbackLockStore) TryLock(ctx context.Context, key, value string, ttl time.Duration) (bool, error) {
	ok, err := f.primary.TryLock(ctx, key, value, ttl)
	if err == nil {
		return ok, nil
	}
	return f.secondary.TryLock(ctx, key, value, ttl)
}

func (f *fallbackLockStore) Unlock(ctx context.Context, key, value string) error {
	if err := f.primary.Unlock(ctx, key, value); err == nil {
		return nil
	}
	return f.secondary.Unlock(ctx, key, value)
}

func (f *fallbackLockStore) Get(ctx context.Context, key string) (string, error) {
	value, err := f.primary.Get(ctx, key)
	if err == nil {
		return value, nil
	}
	return f.secondary.Get(ctx, key)
}

func (f *fallbackLockStore) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	if err := f.primary.Set(ctx, key, value, ttl); err == nil {
		return nil
	}
	return f.secondary.Set(ctx, key, value, ttl)
}

func (f *fallbackLockStore) Delete(ctx context.Context, key string) error {
	if err := f.primary.Delete(ctx, key); err == nil {
		return nil
	}
	return f.secondary.Delete(ctx, key)
}

type distributedCoordinator struct {
	docker            *dockerapi.Client
	runtime           swarmRuntime
	clusterID         string
	store             lockStore
	lockTTL           time.Duration
	stateTTL          time.Duration
	registratorSvcID  string
	advertiseMode     string
	advertiseOverride string
	managerAPIPort    int
	managerOnly       bool
}

func newDistributedCoordinator(docker *dockerapi.Client, runtime swarmRuntime, managerOnly bool, advertiseMode, advertiseOverride, redisAddr, clusterID string, managerAPIPort int) *distributedCoordinator {
	memStore := newMemoryLockStore()
	var store lockStore = memStore
	if redisAddr != "" {
		store = &fallbackLockStore{
			primary:   newRedisLockStore(redisAddr),
			secondary: memStore,
		}
	}
	if clusterID == "" {
		clusterID = clusterIDFrom(runtime)
	}
	return &distributedCoordinator{
		docker:            docker,
		runtime:           runtime,
		clusterID:         clusterID,
		store:             store,
		lockTTL:           30 * time.Second,
		stateTTL:          10 * time.Minute,
		registratorSvcID:  runtime.SwarmServiceID,
		advertiseMode:     advertiseMode,
		advertiseOverride: advertiseOverride,
		managerAPIPort:    managerAPIPort,
		managerOnly:       managerOnly,
	}
}

func clusterIDFrom(runtime swarmRuntime) string {
	if runtime.NodeID != "" {
		return runtime.NodeID[:min(clusterIDPrefixLen, len(runtime.NodeID))]
	}
	return "default"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (d *distributedCoordinator) OwnsContainer(container *dockerapi.Container) bool {
	if container == nil || container.Config == nil {
		return true
	}
	serviceID := container.Config.Labels["com.docker.swarm.service.id"]
	if serviceID == "" {
		return true
	}
	if !d.runtime.Enabled {
		return true
	}
	if d.runtime.Role != "manager" && d.managerOnly {
		return false
	}
	owner := d.ownerForService(serviceID)
	return owner == "" || owner == d.runtime.NodeID
}

func (d *distributedCoordinator) ownerForService(serviceID string) string {
	managerIDs := d.managerNodeIDs()
	if len(managerIDs) > 0 {
		return deterministicOwnerNode(serviceID, managerIDs)
	}
	peerIDs := d.peerNodeIDs()
	if len(peerIDs) > 0 {
		return deterministicOwnerNode(serviceID, peerIDs)
	}
	return ""
}

func deterministicOwnerNode(serviceID string, nodeIDs []string) string {
	if len(nodeIDs) == 0 {
		return ""
	}
	sorted := append([]string{}, nodeIDs...)
	sort.Strings(sorted)
	return sorted[hashIndex(serviceID, len(sorted))]
}

func hashIndex(value string, size int) int {
	if size == 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(value))
	return int(h.Sum32() % uint32(size))
}

func (d *distributedCoordinator) managerNodeIDs() []string {
	nodes, err := d.docker.ListNodes(dockerapi.ListNodesOptions{})
	if err != nil {
		logrus.WithError(err).Warn("unable to list swarm managers for ownership")
		return nil
	}
	ids := make([]string, 0)
	for _, node := range nodes {
		if node.ManagerStatus != nil {
			ids = append(ids, node.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func (d *distributedCoordinator) peerNodeIDs() []string {
	if d.registratorSvcID == "" {
		return nil
	}
	tasks, err := d.docker.ListTasks(dockerapi.ListTasksOptions{
		Filters: map[string][]string{
			"service":       {d.registratorSvcID},
			"desired-state": {"running"},
		},
	})
	if err != nil {
		logrus.WithError(err).Warn("unable to list registrator tasks for peer discovery")
		return nil
	}
	set := make(map[string]struct{})
	for _, task := range tasks {
		if task.NodeID != "" {
			set[task.NodeID] = struct{}{}
		}
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (d *distributedCoordinator) BeforeRegister(service *bridge.Service, fingerprint string) (bool, error) {
	ctx := context.Background()
	lockKey := d.lockKey(service.ID)
	stateKey := d.stateKey(service.ID)
	ownerValue := d.runtime.NodeID
	if ownerValue == "" {
		ownerValue = "standalone"
	}
	locked, err := d.withBackoffLock(ctx, lockKey, ownerValue)
	if err != nil {
		logrus.WithError(err).Warn("lock acquisition failed; falling back to allow register")
		return true, nil
	}
	if !locked {
		return false, nil
	}
	existing, err := d.store.Get(ctx, stateKey)
	if err != nil {
		return true, nil
	}
	if existing == fingerprint {
		_ = d.store.Unlock(ctx, lockKey, ownerValue)
		return false, nil
	}
	return true, nil
}

func (d *distributedCoordinator) AfterRegister(service *bridge.Service, fingerprint string) error {
	ctx := context.Background()
	lockKey := d.lockKey(service.ID)
	stateKey := d.stateKey(service.ID)
	ownerValue := d.runtime.NodeID
	if ownerValue == "" {
		ownerValue = "standalone"
	}
	if err := d.store.Set(ctx, stateKey, fingerprint, d.stateTTL); err != nil {
		logrus.WithError(err).Warn("failed to store service fingerprint")
	}
	return d.store.Unlock(ctx, lockKey, ownerValue)
}

func (d *distributedCoordinator) BeforeDeregister(service *bridge.Service) (bool, error) {
	ctx := context.Background()
	lockKey := d.lockKey(service.ID)
	ownerValue := d.runtime.NodeID
	if ownerValue == "" {
		ownerValue = "standalone"
	}
	locked, err := d.withBackoffLock(ctx, lockKey, ownerValue)
	if err != nil {
		logrus.WithError(err).Warn("deregister lock acquisition failed; allowing operation")
		return true, nil
	}
	return locked, nil
}

func (d *distributedCoordinator) AfterDeregister(service *bridge.Service) error {
	ctx := context.Background()
	lockKey := d.lockKey(service.ID)
	stateKey := d.stateKey(service.ID)
	ownerValue := d.runtime.NodeID
	if ownerValue == "" {
		ownerValue = "standalone"
	}
	_ = d.store.Delete(ctx, stateKey)
	return d.store.Unlock(ctx, lockKey, ownerValue)
}

func (d *distributedCoordinator) withBackoffLock(ctx context.Context, key, value string) (bool, error) {
	var locked bool
	op := func() error {
		ok, err := d.store.TryLock(ctx, key, value, d.lockTTL)
		if err != nil {
			return err
		}
		locked = ok
		return nil
	}
	exp := backoff.NewExponentialBackOff()
	exp.MaxElapsedTime = lockRetryMaxElapsed
	if err := backoff.Retry(op, exp); err != nil {
		return false, err
	}
	return locked, nil
}

func (d *distributedCoordinator) lockKey(serviceID string) string {
	return fmt.Sprintf("registrator:%s:lock:%s", d.clusterID, serviceID)
}

func (d *distributedCoordinator) stateKey(serviceID string) string {
	return fmt.Sprintf("registrator:%s:state:%s", d.clusterID, serviceID)
}

func (d *distributedCoordinator) ResolveSwarmPorts(container *dockerapi.Container) ([]bridge.ServicePort, error) {
	if container == nil || container.Config == nil {
		return nil, nil
	}
	serviceID := container.Config.Labels["com.docker.swarm.service.id"]
	if serviceID == "" {
		return nil, nil
	}
	service, err := d.inspectService(serviceID)
	if err != nil {
		return nil, err
	}
	ports := service.Spec.EndpointSpec.Ports
	if len(ports) == 0 {
		ports = service.Endpoint.Ports
	}
	out := make([]bridge.ServicePort, 0, len(ports))
	for _, p := range ports {
		if p.PublishedPort == 0 && p.TargetPort == 0 {
			continue
		}
		hostIP := d.advertisedIP(service)
		if hostIP == "" {
			hostIP = d.runtime.NodeAddr
		}
		portType := "tcp"
		if string(p.Protocol) != "" {
			portType = string(p.Protocol)
		}
		out = append(out, bridge.NewResolvedServicePort(
			container,
			hostIP,
			fmt.Sprintf("%d", p.PublishedPort),
			fmt.Sprintf("%d", p.TargetPort),
			portType,
		))
	}
	return out, nil
}

func (d *distributedCoordinator) inspectService(serviceID string) (*swarmapi.Service, error) {
	if d.runtime.Role == "manager" {
		return d.docker.InspectService(serviceID)
	}
	// Worker-to-manager metadata lookups currently use a direct Docker API TCP endpoint.
	// Deployments should secure this path (private network, firewall policy, or TLS-terminating proxy).
	managers := d.managerNodeAddrs()
	var service *swarmapi.Service
	op := func() error {
		for _, addr := range managers {
			client, err := dockerapi.NewVersionedClient(fmt.Sprintf("tcp://%s:%d", addr, d.managerAPIPort), defaultDockerAPIVer)
			if err != nil {
				continue
			}
			service, err = client.InspectService(serviceID)
			if err == nil {
				return nil
			}
		}
		return fmt.Errorf("unable to inspect service %s from manager list", serviceID)
	}
	exp := backoff.NewExponentialBackOff()
	exp.MaxElapsedTime = managerRetryMaxElapsed
	err := backoff.Retry(op, exp)
	return service, err
}

func (d *distributedCoordinator) managerNodeAddrs() []string {
	nodes, err := d.docker.ListNodes(dockerapi.ListNodesOptions{})
	if err != nil {
		return nil
	}
	addrs := make([]string, 0)
	for _, node := range nodes {
		if node.ManagerStatus != nil && node.Status.Addr != "" {
			addrs = append(addrs, node.Status.Addr)
		}
	}
	sort.Strings(addrs)
	return addrs
}

func (d *distributedCoordinator) advertisedIP(service *swarmapi.Service) string {
	switch d.advertiseMode {
	case "custom":
		return d.advertiseOverride
	case "service-vip":
		if len(service.Endpoint.VirtualIPs) == 0 {
			return ""
		}
		addr := service.Endpoint.VirtualIPs[0].Addr
		if idx := strings.Index(addr, "/"); idx >= 0 {
			return addr[:idx]
		}
		return addr
	default:
		if d.advertiseOverride != "" {
			return d.advertiseOverride
		}
		return d.runtime.NodeAddr
	}
}
