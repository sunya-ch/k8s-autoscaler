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

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/labels"
	autoscalinglisters "k8s.io/client-go/listers/autoscaling/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CtrlHPALister implements autoscalinglisters.HorizontalPodAutoscalerLister using ctrl.Client.
type CtrlHPALister struct{ client.Client }

func (l *CtrlHPALister) List(_ labels.Selector) ([]*autoscalingv2.HorizontalPodAutoscaler, error) {
	list := &autoscalingv2.HorizontalPodAutoscalerList{}
	if err := l.Client.List(context.Background(), list); err != nil {
		return nil, err
	}
	result := make([]*autoscalingv2.HorizontalPodAutoscaler, len(list.Items))
	for i := range list.Items {
		result[i] = &list.Items[i]
	}
	return result, nil
}

func (l *CtrlHPALister) HorizontalPodAutoscalers(namespace string) autoscalinglisters.HorizontalPodAutoscalerNamespaceLister {
	return &ctrlHPANamespaceLister{client: l.Client, namespace: namespace}
}

type ctrlHPANamespaceLister struct {
	client    client.Client
	namespace string
}

func (l *ctrlHPANamespaceLister) List(_ labels.Selector) ([]*autoscalingv2.HorizontalPodAutoscaler, error) {
	list := &autoscalingv2.HorizontalPodAutoscalerList{}
	if err := l.client.List(context.Background(), list, client.InNamespace(l.namespace)); err != nil {
		return nil, err
	}
	result := make([]*autoscalingv2.HorizontalPodAutoscaler, len(list.Items))
	for i := range list.Items {
		result[i] = &list.Items[i]
	}
	return result, nil
}

func (l *ctrlHPANamespaceLister) Get(name string) (*autoscalingv2.HorizontalPodAutoscaler, error) {
	obj := &autoscalingv2.HorizontalPodAutoscaler{}
	if err := l.client.Get(context.Background(), client.ObjectKey{Namespace: l.namespace, Name: name}, obj); err != nil {
		return nil, err
	}
	return obj, nil
}
