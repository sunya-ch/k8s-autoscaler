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
	"k8s.io/apimachinery/pkg/labels"
	mpav1alpha1 "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1alpha1"
	"k8s.io/client-go/listers"
	"k8s.io/client-go/tools/cache"
)

// MultidimPodAutoscalerLister helps list MultidimPodAutoscalers.
// All objects returned here must be treated as read-only.
type MultidimPodAutoscalerLister interface {
	// List lists all MultidimPodAutoscalers in the indexer.
	List(selector labels.Selector) (ret []*mpav1alpha1.MultidimPodAutoscaler, err error)
	// MultidimPodAutoscalers returns an object that can list and get MultidimPodAutoscalers.
	MultidimPodAutoscalers(namespace string) MultidimPodAutoscalerNamespaceLister
	MultidimPodAutoscalerListerExpansion
}

// MultidimPodAutoscalerListerExpansion is a placeholder for additional methods.
type MultidimPodAutoscalerListerExpansion interface{}

// multidimPodAutoscalerLister implements MultidimPodAutoscalerLister.
type multidimPodAutoscalerLister struct {
	listers.ResourceIndexer[*mpav1alpha1.MultidimPodAutoscaler]
}

// NewMultidimPodAutoscalerLister returns a new MultidimPodAutoscalerLister.
func NewMultidimPodAutoscalerLister(indexer cache.Indexer) MultidimPodAutoscalerLister {
	return &multidimPodAutoscalerLister{
		listers.New[*mpav1alpha1.MultidimPodAutoscaler](indexer, mpav1alpha1.Resource("multidimpodautoscalers")),
	}
}

// MultidimPodAutoscalers returns a namespace-scoped lister.
func (s *multidimPodAutoscalerLister) MultidimPodAutoscalers(namespace string) MultidimPodAutoscalerNamespaceLister {
	return multidimPodAutoscalerNamespaceLister{
		listers.NewNamespaced[*mpav1alpha1.MultidimPodAutoscaler](s.ResourceIndexer, namespace),
	}
}

// MultidimPodAutoscalerNamespaceLister helps list and get MultidimPodAutoscalers in a namespace.
// All objects returned here must be treated as read-only.
type MultidimPodAutoscalerNamespaceLister interface {
	// List lists all MultidimPodAutoscalers in the indexer for a given namespace.
	List(selector labels.Selector) (ret []*mpav1alpha1.MultidimPodAutoscaler, err error)
	// Get retrieves the MultidimPodAutoscaler from the indexer for a given namespace and name.
	Get(name string) (*mpav1alpha1.MultidimPodAutoscaler, error)
	MultidimPodAutoscalerNamespaceListerExpansion
}

// MultidimPodAutoscalerNamespaceListerExpansion is a placeholder for additional methods.
type MultidimPodAutoscalerNamespaceListerExpansion interface{}

// multidimPodAutoscalerNamespaceLister implements MultidimPodAutoscalerNamespaceLister.
type multidimPodAutoscalerNamespaceLister struct {
	listers.ResourceIndexer[*mpav1alpha1.MultidimPodAutoscaler]
}
