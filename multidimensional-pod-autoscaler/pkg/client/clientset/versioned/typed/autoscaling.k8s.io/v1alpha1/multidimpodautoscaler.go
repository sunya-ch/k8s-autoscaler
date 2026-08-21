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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	mpav1alpha1 "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1alpha1"
	"k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/client/clientset/versioned/scheme"
	"k8s.io/client-go/gentype"
)

// MultidimPodAutoscalersGetter has a method to return a MultidimPodAutoscalerInterface.
type MultidimPodAutoscalersGetter interface {
	MultidimPodAutoscalers(namespace string) MultidimPodAutoscalerInterface
}

// MultidimPodAutoscalerInterface has methods to work with MultidimPodAutoscaler resources.
type MultidimPodAutoscalerInterface interface {
	Create(ctx context.Context, mpa *mpav1alpha1.MultidimPodAutoscaler, opts metav1.CreateOptions) (*mpav1alpha1.MultidimPodAutoscaler, error)
	Update(ctx context.Context, mpa *mpav1alpha1.MultidimPodAutoscaler, opts metav1.UpdateOptions) (*mpav1alpha1.MultidimPodAutoscaler, error)
	UpdateStatus(ctx context.Context, mpa *mpav1alpha1.MultidimPodAutoscaler, opts metav1.UpdateOptions) (*mpav1alpha1.MultidimPodAutoscaler, error)
	Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error
	DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOpts metav1.ListOptions) error
	Get(ctx context.Context, name string, opts metav1.GetOptions) (*mpav1alpha1.MultidimPodAutoscaler, error)
	List(ctx context.Context, opts metav1.ListOptions) (*mpav1alpha1.MultidimPodAutoscalerList, error)
	Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error)
	Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*mpav1alpha1.MultidimPodAutoscaler, error)
	MultidimPodAutoscalerExpansion
}

// MultidimPodAutoscalerExpansion is a placeholder for additional hand-crafted methods.
type MultidimPodAutoscalerExpansion interface{}

// multidimPodAutoscalers implements MultidimPodAutoscalerInterface.
type multidimPodAutoscalers struct {
	*gentype.ClientWithList[*mpav1alpha1.MultidimPodAutoscaler, *mpav1alpha1.MultidimPodAutoscalerList]
}

// newMultidimPodAutoscalers returns a MultidimPodAutoscalers client.
func newMultidimPodAutoscalers(c *AutoscalingV1alpha1Client, namespace string) *multidimPodAutoscalers {
	return &multidimPodAutoscalers{
		gentype.NewClientWithList[*mpav1alpha1.MultidimPodAutoscaler, *mpav1alpha1.MultidimPodAutoscalerList](
			"multidimpodautoscalers",
			c.RESTClient(),
			scheme.ParameterCodec,
			namespace,
			func() *mpav1alpha1.MultidimPodAutoscaler { return &mpav1alpha1.MultidimPodAutoscaler{} },
			func() *mpav1alpha1.MultidimPodAutoscalerList { return &mpav1alpha1.MultidimPodAutoscalerList{} },
		),
	}
}
