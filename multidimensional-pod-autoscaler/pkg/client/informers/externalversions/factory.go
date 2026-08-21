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

package externalversions

import (
	"context"
	"reflect"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	versioned "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/client/clientset/versioned"
	autoscalingv1alpha1 "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/client/informers/externalversions/autoscaling.k8s.io/v1alpha1"
	internalinterfaces "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/client/informers/externalversions/internalinterfaces"
	"k8s.io/client-go/tools/cache"
)

// SharedInformerOption defines the functional option type for SharedInformerFactory.
type SharedInformerOption func(*sharedInformerFactory) *sharedInformerFactory

type sharedInformerFactory struct {
	client           versioned.Interface
	namespace        string
	tweakListOptions internalinterfaces.TweakListOptionsFunc
	lock             sync.Mutex
	defaultResync    time.Duration
	customResync     map[reflect.Type]time.Duration
	informers        map[reflect.Type]cache.SharedIndexInformer
	startedInformers map[reflect.Type]bool
	wg               sync.WaitGroup
	shuttingDown     bool
}

// SharedInformerFactory is a factory for creating shared informers for MPA resources.
type SharedInformerFactory interface {
	internalinterfaces.SharedInformerFactory

	// AutoscalingV1alpha1 returns an informer interface for the MPA v1alpha1 group.
	AutoscalingV1alpha1() autoscalingv1alpha1.Interface
}

// WithNamespace restricts the factory to the given namespace.
func WithNamespace(namespace string) SharedInformerOption {
	return func(factory *sharedInformerFactory) *sharedInformerFactory {
		factory.namespace = namespace
		return factory
	}
}

// WithTweakListOptions sets the tweak list options on the factory.
func WithTweakListOptions(tweakListOptions internalinterfaces.TweakListOptionsFunc) SharedInformerOption {
	return func(factory *sharedInformerFactory) *sharedInformerFactory {
		factory.tweakListOptions = tweakListOptions
		return factory
	}
}

// NewSharedInformerFactory constructs a new instance of SharedInformerFactory for all namespaces.
func NewSharedInformerFactory(client versioned.Interface, defaultResync time.Duration) SharedInformerFactory {
	return NewSharedInformerFactoryWithOptions(client, defaultResync)
}

// NewFilteredSharedInformerFactory constructs a new instance of SharedInformerFactory.
// Listers obtained via this factory will be subject to the same filters as specified here.
func NewFilteredSharedInformerFactory(
	client versioned.Interface,
	defaultResync time.Duration,
	namespace string,
	tweakListOptions internalinterfaces.TweakListOptionsFunc,
) SharedInformerFactory {
	return NewSharedInformerFactoryWithOptions(
		client,
		defaultResync,
		WithNamespace(namespace),
		WithTweakListOptions(tweakListOptions),
	)
}

// NewSharedInformerFactoryWithOptions constructs a new SharedInformerFactory with options.
func NewSharedInformerFactoryWithOptions(
	client versioned.Interface,
	defaultResync time.Duration,
	options ...SharedInformerOption,
) SharedInformerFactory {
	factory := &sharedInformerFactory{
		client:           client,
		namespace:        metav1.NamespaceAll,
		defaultResync:    defaultResync,
		informers:        make(map[reflect.Type]cache.SharedIndexInformer),
		startedInformers: make(map[reflect.Type]bool),
		customResync:     make(map[reflect.Type]time.Duration),
	}
	for _, opt := range options {
		factory = opt(factory)
	}
	return factory
}

// Start initialises all registered informers.
func (f *sharedInformerFactory) Start(stopCh <-chan struct{}) {
	f.lock.Lock()
	defer f.lock.Unlock()
	if f.shuttingDown {
		return
	}
	for informerType, informer := range f.informers {
		if !f.startedInformers[informerType] {
			f.wg.Add(1)
			go func() {
				defer f.wg.Done()
				informer.Run(stopCh)
			}()
			f.startedInformers[informerType] = true
		}
	}
}

// WaitForCacheSync waits for all started informers' caches to sync.
func (f *sharedInformerFactory) WaitForCacheSync(stopCh <-chan struct{}) map[reflect.Type]bool {
	informers := func() map[reflect.Type]cache.SharedIndexInformer {
		f.lock.Lock()
		defer f.lock.Unlock()
		result := make(map[reflect.Type]cache.SharedIndexInformer, len(f.informers))
		for key, informer := range f.informers {
			if f.startedInformers[key] {
				result[key] = informer
			}
		}
		return result
	}()

	result := make(map[reflect.Type]bool, len(informers))
	for informType, informer := range informers {
		result[informType] = cache.WaitForCacheSync(stopCh, informer.HasSynced)
	}
	return result
}

// InformerFor returns a shared informer for the given object type.
func (f *sharedInformerFactory) InformerFor(obj runtime.Object, newFunc internalinterfaces.NewInformerFunc) cache.SharedIndexInformer {
	f.lock.Lock()
	defer f.lock.Unlock()

	informerType := reflect.TypeOf(obj)
	informer, exists := f.informers[informerType]
	if exists {
		return informer
	}

	resyncPeriod, exists := f.customResync[informerType]
	if !exists {
		resyncPeriod = f.defaultResync
	}

	informer = newFunc(f.client, resyncPeriod)
	f.informers[informerType] = informer
	return informer
}

// InformerName returns the InformerName for this factory (nil = use GVR-based naming).
func (f *sharedInformerFactory) InformerName() *cache.InformerName {
	return nil
}

// AutoscalingV1alpha1 returns the autoscaling.k8s.io/v1alpha1 group informers.
func (f *sharedInformerFactory) AutoscalingV1alpha1() autoscalingv1alpha1.Interface {
	return autoscalingv1alpha1.New(f, f.namespace, f.tweakListOptions)
}

// Shutdown marks a factory as shutting down.
func (f *sharedInformerFactory) Shutdown() {
	f.lock.Lock()
	f.shuttingDown = true
	f.lock.Unlock()
	f.wg.Wait()
}

// ForResource returns a generic informer for the requested resource, if registered.
func (f *sharedInformerFactory) ForResource(resource schema.GroupVersionResource) (GenericInformer, error) {
	return f.ForResourceWithContext(context.Background(), resource)
}

// ForResourceWithContext returns a generic informer for the requested resource.
func (f *sharedInformerFactory) ForResourceWithContext(_ context.Context, resource schema.GroupVersionResource) (GenericInformer, error) {
	return newGenericInformer(f, resource)
}
