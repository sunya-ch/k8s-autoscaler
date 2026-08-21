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
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	mpav1alpha1 "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1alpha1"
	"k8s.io/client-go/tools/cache"
)

// GenericInformer is type of SharedIndexInformer which will locate and delegate to other
// generic informers based on type.
type GenericInformer interface {
	Informer() cache.SharedIndexInformer
	Lister() cache.GenericNamespaceLister
}

type genericInformer struct {
	informer cache.SharedIndexInformer
	resource schema.GroupResource
}

func (f *genericInformer) Informer() cache.SharedIndexInformer {
	return f.informer
}

func (f *genericInformer) Lister() cache.GenericNamespaceLister {
	return cache.NewGenericLister(f.informer.GetIndexer(), f.resource).ByNamespace(metav1.NamespaceAll)
}

func newGenericInformer(f *sharedInformerFactory, resource schema.GroupVersionResource) (GenericInformer, error) {
	switch resource {
	case mpav1alpha1.SchemeGroupVersion.WithResource("multidimpodautoscalers"):
		return &genericInformer{
			informer: f.AutoscalingV1alpha1().MultidimPodAutoscalers().Informer(),
			resource: resource.GroupResource(),
		}, nil
	}
	return nil, fmt.Errorf("no informer found for %v", resource)
}
