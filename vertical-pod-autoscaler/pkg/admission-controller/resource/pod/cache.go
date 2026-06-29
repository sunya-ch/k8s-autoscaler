/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package pod

import (
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	vpa_types "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
)

// entryCacheTTL is how long a VPA cache entry lives after it was stored.
// ResourceClaims for a pod are admitted within milliseconds of the pod being
// created, so 30 s is a very generous upper bound while still preventing the
// map from growing forever if a pod is admitted but its ResourceClaims never
// arrive (e.g. admission was rejected downstream).
const entryCacheTTL = 30 * time.Second

// VpaCacheEntry holds the VPA and the pod that were matched during pod admission.
// Both are needed by the ResourceClaim handler: the VPA for recommendation data
// and the pod's Spec.ResourceClaims for claim-template-name matching.
type VpaCacheEntry struct {
	Vpa *vpa_types.VerticalPodAutoscaler
	Pod *corev1.Pod
}

// VpaCache is a thread-safe store keyed by "namespace/generateName" (the pod's
// name prefix without the random suffix).
//
// At pod admission time no UID or final name exists yet; only the generateName
// prefix is stable. At ResourceClaim admission time the final pod name is
// available in ownerRef.Name — keyFromFinalName strips the Kubernetes-appended
// random suffix to reconstruct the same prefix key.
//
// Entries are NOT removed on Get because a pod may own multiple ResourceClaims
// (one per claim template) each arriving in a separate admission request.
// time.AfterFunc schedules deletion of each entry individually after
// entryCacheTTL, so the map self-cleans without scanning or a background goroutine.
type VpaCache struct {
	mu      sync.Mutex
	entries map[string]VpaCacheEntry
}

// NewVpaCache creates an empty VpaCache.
func NewVpaCache() *VpaCache {
	return &VpaCache{
		entries: make(map[string]VpaCacheEntry),
	}
}

// Store records that the given VPA controls the pod identified by namespace and
// name (which is "generateName + %" at pod admission time). A timer is armed to
// delete the entry after entryCacheTTL so the map self-cleans without scanning.
func (c *VpaCache) Store(namespace, name string, matchedVpa *vpa_types.VerticalPodAutoscaler, pod *corev1.Pod) {
	key := namespace + "/" + name
	c.mu.Lock()
	c.entries[key] = VpaCacheEntry{Vpa: matchedVpa, Pod: pod}
	c.mu.Unlock()

	time.AfterFunc(entryCacheTTL, func() {
		c.mu.Lock()
		delete(c.entries, key)
		c.mu.Unlock()
	})
}

// Get returns the cached entry for the pod identified by namespace and its final
// assigned name. It first tries an exact key lookup (for pods with an explicit
// name), then falls back to reconstructing the "namespace/generateName%" key by
// stripping the random suffix (for generateName pods). Returns nil if no live
// entry exists. The entry is NOT removed so that multiple ResourceClaim webhooks
// for the same pod can all find it.
func (c *VpaCache) Get(namespace, finalName string) *VpaCacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Fast path: pod was stored with an explicit name.
	if entry, ok := c.entries[namespace+"/"+finalName]; ok {
		return &entry
	}

	// Slow path: pod was stored with generateName — reconstruct the prefix key.
	if key := namespace + "/" + keyFromFinalName(finalName); key != namespace+"/"+finalName {
		if entry, ok := c.entries[key]; ok {
			return &entry
		}
	}
	return nil
}

// keyFromFinalName converts a fully-assigned pod name back to the generateName
// prefix used as the cache key. Kubernetes always appends a "-<5-char-suffix>"
// via generateName, so stripping everything after the last "-" recovers the
// prefix (e.g. "mypod-x7k2p" → "mypod-"). For pods with an explicit name
// (no "-") the name is returned unchanged.
func keyFromFinalName(name string) string {
	if i := strings.LastIndex(name, "-"); i >= 0 {
		return name[:i+1] + "%"
	}
	return name
}
