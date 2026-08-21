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

package v1alpha1

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	mpav1alpha1 "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1alpha1"
	versioned "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/client/clientset/versioned"
	internalinterfaces "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/client/informers/externalversions/internalinterfaces"
	mpalisters "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/client/listers/autoscaling.k8s.io/v1alpha1"
	"k8s.io/client-go/tools/cache"
)

// MultidimPodAutoscalerInformer provides access to a shared informer and lister for
// MultidimPodAutoscalers.
type MultidimPodAutoscalerInformer interface {
	Informer() cache.SharedIndexInformer
	Lister() mpalisters.MultidimPodAutoscalerLister
}

type multidimPodAutoscalerInformer struct {
	factory          internalinterfaces.SharedInformerFactory
	tweakListOptions internalinterfaces.TweakListOptionsFunc
	namespace        string
}

// NewMultidimPodAutoscalerInformer constructs a new informer for MultidimPodAutoscaler type.
// Always prefer using an informer factory to get a shared informer instead of an independent one.
func NewMultidimPodAutoscalerInformer(
	client versioned.Interface,
	namespace string,
	resyncPeriod time.Duration,
	indexers cache.Indexers,
) cache.SharedIndexInformer {
	return NewFilteredMultidimPodAutoscalerInformer(client, namespace, resyncPeriod, indexers, nil)
}

// NewFilteredMultidimPodAutoscalerInformer constructs a new filtered informer for
// MultidimPodAutoscaler type.
func NewFilteredMultidimPodAutoscalerInformer(
	client versioned.Interface,
	namespace string,
	resyncPeriod time.Duration,
	indexers cache.Indexers,
	tweakListOptions internalinterfaces.TweakListOptionsFunc,
) cache.SharedIndexInformer {
	return cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(opts metav1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&opts)
				}
				return client.AutoscalingV1alpha1().MultidimPodAutoscalers(namespace).List(context.Background(), opts)
			},
			WatchFunc: func(opts metav1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&opts)
				}
				return client.AutoscalingV1alpha1().MultidimPodAutoscalers(namespace).Watch(context.Background(), opts)
			},
		},
		&mpav1alpha1.MultidimPodAutoscaler{},
		resyncPeriod,
		indexers,
	)
}

func (f *multidimPodAutoscalerInformer) defaultInformer(client versioned.Interface, resyncPeriod time.Duration) cache.SharedIndexInformer {
	return NewFilteredMultidimPodAutoscalerInformer(
		client,
		f.namespace,
		resyncPeriod,
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
		f.tweakListOptions,
	)
}

func (f *multidimPodAutoscalerInformer) Informer() cache.SharedIndexInformer {
	return f.factory.InformerFor(&mpav1alpha1.MultidimPodAutoscaler{}, f.defaultInformer)
}

func (f *multidimPodAutoscalerInformer) Lister() mpalisters.MultidimPodAutoscalerLister {
	return mpalisters.NewMultidimPodAutoscalerLister(f.Informer().GetIndexer())
}
