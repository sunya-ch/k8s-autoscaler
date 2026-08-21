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
	"net/http"

	mpav1alpha1 "k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1alpha1"
	"k8s.io/autoscaler/multidimensional-pod-autoscaler/pkg/client/clientset/versioned/scheme"
	"k8s.io/client-go/rest"
)

// AutoscalingV1alpha1Interface is the interface for the MPA v1alpha1 group client.
type AutoscalingV1alpha1Interface interface {
	RESTClient() rest.Interface
	MultidimPodAutoscalersGetter
}

// AutoscalingV1alpha1Client is used to interact with the autoscaling.k8s.io/v1alpha1 group.
type AutoscalingV1alpha1Client struct {
	restClient rest.Interface
}

// MultidimPodAutoscalers returns a MultidimPodAutoscalerInterface for the given namespace.
func (c *AutoscalingV1alpha1Client) MultidimPodAutoscalers(namespace string) MultidimPodAutoscalerInterface {
	return newMultidimPodAutoscalers(c, namespace)
}

// NewForConfig creates a new AutoscalingV1alpha1Client for the given config.
func NewForConfig(c *rest.Config) (*AutoscalingV1alpha1Client, error) {
	config := *c
	setConfigDefaults(&config)
	httpClient, err := rest.HTTPClientFor(&config)
	if err != nil {
		return nil, err
	}
	return NewForConfigAndClient(&config, httpClient)
}

// NewForConfigAndClient creates a new AutoscalingV1alpha1Client for the given config and http client.
func NewForConfigAndClient(c *rest.Config, h *http.Client) (*AutoscalingV1alpha1Client, error) {
	config := *c
	setConfigDefaults(&config)
	client, err := rest.RESTClientForConfigAndClient(&config, h)
	if err != nil {
		return nil, err
	}
	return &AutoscalingV1alpha1Client{client}, nil
}

// NewForConfigOrDie creates a new AutoscalingV1alpha1Client for the given config and panics on error.
func NewForConfigOrDie(c *rest.Config) *AutoscalingV1alpha1Client {
	client, err := NewForConfig(c)
	if err != nil {
		panic(err)
	}
	return client
}

// New creates a new AutoscalingV1alpha1Client for the given RESTClient.
func New(c rest.Interface) *AutoscalingV1alpha1Client {
	return &AutoscalingV1alpha1Client{c}
}

func setConfigDefaults(config *rest.Config) {
	gv := mpav1alpha1.SchemeGroupVersion
	config.GroupVersion = &gv
	config.APIPath = "/apis"
	config.NegotiatedSerializer = rest.CodecFactoryForGeneratedClient(scheme.Scheme, scheme.Codecs).WithoutConversion()
	if config.UserAgent == "" {
		config.UserAgent = rest.DefaultKubernetesUserAgent()
	}
}

// RESTClient returns the underlying REST client.
func (c *AutoscalingV1alpha1Client) RESTClient() rest.Interface {
	if c == nil {
		return nil
	}
	return c.restClient
}
