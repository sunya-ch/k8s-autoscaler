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

package lister

import (
	"context"

	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mpav1alpha1 "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1alpha1"
	mpalisters "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/client/listers/autoscaling.k8s.io/v1alpha1"
)

// CtrlMPALister implements mpalisters.MultidimPodAutoscalerLister using ctrl.Client.
type CtrlMPALister struct{ client.Client }

func (l *CtrlMPALister) List(_ labels.Selector) ([]*mpav1alpha1.MultidimPodAutoscaler, error) {
	list := &mpav1alpha1.MultidimPodAutoscalerList{}
	if err := l.Client.List(context.Background(), list); err != nil {
		return nil, err
	}
	result := make([]*mpav1alpha1.MultidimPodAutoscaler, len(list.Items))
	for i := range list.Items {
		result[i] = &list.Items[i]
	}
	return result, nil
}

func (l *CtrlMPALister) MultidimPodAutoscalers(namespace string) mpalisters.MultidimPodAutoscalerNamespaceLister {
	return &ctrlMPANamespaceLister{client: l.Client, namespace: namespace}
}

type ctrlMPANamespaceLister struct {
	client    client.Client
	namespace string
}

func (l *ctrlMPANamespaceLister) List(_ labels.Selector) ([]*mpav1alpha1.MultidimPodAutoscaler, error) {
	list := &mpav1alpha1.MultidimPodAutoscalerList{}
	if err := l.client.List(context.Background(), list, client.InNamespace(l.namespace)); err != nil {
		return nil, err
	}
	result := make([]*mpav1alpha1.MultidimPodAutoscaler, len(list.Items))
	for i := range list.Items {
		result[i] = &list.Items[i]
	}
	return result, nil
}

func (l *ctrlMPANamespaceLister) Get(name string) (*mpav1alpha1.MultidimPodAutoscaler, error) {
	obj := &mpav1alpha1.MultidimPodAutoscaler{}
	if err := l.client.Get(context.Background(), client.ObjectKey{Namespace: l.namespace, Name: name}, obj); err != nil {
		return nil, err
	}
	return obj, nil
}
