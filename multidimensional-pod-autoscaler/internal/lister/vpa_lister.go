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

	vpav1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	vpalisters "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/client/listers/autoscaling.k8s.io/v1"
)

// CtrlVPALister implements vpalisters.VerticalPodAutoscalerLister using ctrl.Client.
type CtrlVPALister struct{ client.Client }

func (l *CtrlVPALister) List(_ labels.Selector) ([]*vpav1.VerticalPodAutoscaler, error) {
	list := &vpav1.VerticalPodAutoscalerList{}
	if err := l.Client.List(context.Background(), list); err != nil {
		return nil, err
	}
	result := make([]*vpav1.VerticalPodAutoscaler, len(list.Items))
	for i := range list.Items {
		result[i] = &list.Items[i]
	}
	return result, nil
}

func (l *CtrlVPALister) VerticalPodAutoscalers(namespace string) vpalisters.VerticalPodAutoscalerNamespaceLister {
	return &ctrlVPANamespaceLister{client: l.Client, namespace: namespace}
}

type ctrlVPANamespaceLister struct {
	client    client.Client
	namespace string
}

func (l *ctrlVPANamespaceLister) List(_ labels.Selector) ([]*vpav1.VerticalPodAutoscaler, error) {
	list := &vpav1.VerticalPodAutoscalerList{}
	if err := l.client.List(context.Background(), list, client.InNamespace(l.namespace)); err != nil {
		return nil, err
	}
	result := make([]*vpav1.VerticalPodAutoscaler, len(list.Items))
	for i := range list.Items {
		result[i] = &list.Items[i]
	}
	return result, nil
}

func (l *ctrlVPANamespaceLister) Get(name string) (*vpav1.VerticalPodAutoscaler, error) {
	obj := &vpav1.VerticalPodAutoscaler{}
	if err := l.client.Get(context.Background(), client.ObjectKey{Namespace: l.namespace, Name: name}, obj); err != nil {
		return nil, err
	}
	return obj, nil
}
