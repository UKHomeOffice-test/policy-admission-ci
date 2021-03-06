/*
Copyright 2017 Home Office All rights reserved.

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

package kubecertmanager

import (
	"context"
	"testing"
	"time"

	"github.com/UKHomeOffice/policy-admission/pkg/api"

	"github.com/patrickmn/go-cache"
	"github.com/stretchr/testify/assert"
	"k8s.io/api/core/v1"
	extensions "k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/kubernetes/fake"
)

func TestNew(t *testing.T) {
	c, err := New(newTestConfig())
	assert.NotNil(t, c)
	assert.NoError(t, err)
}

func TestNewDefaultConfig(t *testing.T) {
	assert.NotNil(t, NewDefaultConfig())
}

func TestAuthorizer(t *testing.T) {
	config := NewDefaultConfig()
	config.ExternalIngressHostname = "ingress.acp.example.com"
	config.HostedDomains = []string{"example.com"}

	checks := map[string]kubeCertCheck{
		"check that the ingress is allow through": {},
		"check an internally hosted domain is permited": {
			Annotations: map[string]string{"kubernetes.io/ingress.class": "nginx-external"},
			Labels:      map[string]string{"stable.k8s.psg.io/kcm.class": "default"},
			Hosts:       []string{"site.example.com"},
		},
		"check an externaly hosted domain is denied": {
			Annotations: map[string]string{"kubernetes.io/ingress.class": "nginx-external"},
			Labels:      map[string]string{"stable.k8s.psg.io/kcm.class": "default"},
			Hosts:       []string{"site.nohere.com"},
			Errors: field.ErrorList{
				{
					Field:    "spec.rules[0].host",
					BadValue: "site.nohere.com",
					Type:     field.ErrorTypeInvalid,
					Detail:   "domain is not hosted internally and thus denied",
				},
			},
		},
		"check an ingress is allowed when inside the allowed list": {
			Annotations: map[string]string{"kubernetes.io/ingress.class": "nginx-external"},
			Labels:      map[string]string{"stable.k8s.psg.io/kcm.class": "default"},
			Hosts:       []string{"site.example.com"},
		},
		"check an ingress is allowed with a dns annontation and allowed list": {
			Annotations: map[string]string{
				"kubernetes.io/ingress.class":    "nginx-external",
				"stable.k8s.psg.io/kcm.provider": "dns",
			},
			Labels: map[string]string{"stable.k8s.psg.io/kcm.class": "default"},
			Hosts:  []string{"site.example.com"},
		},
		"check an externaly host is denied invalid challenge type": {
			Annotations: map[string]string{
				"kubernetes.io/ingress.class":    "nginx-external",
				"stable.k8s.psg.io/kcm.provider": "bad",
			},
			Labels: map[string]string{"stable.k8s.psg.io/kcm.class": "default"},
			Hosts:  []string{"site.nohere.com"},
			Errors: field.ErrorList{
				{
					Field:    "annotations[stable.k8s.psg.io/kcm.provider]",
					BadValue: "bad",
					Type:     field.ErrorTypeInvalid,
					Detail:   "invalid kube-cert-manager provider type: bad, expected: http",
				},
			},
		},
		"check an externaly host is denied when ingress is not external": {
			Annotations: map[string]string{
				"kubernetes.io/ingress.class":    "nginx-internal",
				"stable.k8s.psg.io/kcm.provider": "http",
			},
			Labels: map[string]string{"stable.k8s.psg.io/kcm.class": "default"},
			Hosts:  []string{"site.nohere.com"},
			Errors: field.ErrorList{
				{
					Field:    "annotations[kubernetes.io/ingress.class]",
					BadValue: "nginx-internal",
					Type:     field.ErrorTypeInvalid,
					Detail:   "invalid kube-cert-manager provider, expected 'nginx-external' for a http challenge",
				},
			},
		},
		"check a ingress is denied when the dns does not resolve": {
			Annotations: map[string]string{
				"kubernetes.io/ingress.class":    "nginx-external",
				"stable.k8s.psg.io/kcm.provider": "http",
			},
			Labels:   map[string]string{"stable.k8s.psg.io/kcm.class": "default"},
			Hosts:    []string{"site.nohere.com"},
			Resolves: "bad.hostname",
			Errors: field.ErrorList{
				{
					Field:    "spec.rules[0].host",
					BadValue: "site.nohere.com",
					Type:     field.ErrorTypeInvalid,
					Detail:   "the hostname: site.nohere.com is not pointed to the external ingress dns name ingress.acp.example.com",
				},
			},
		},
		"check a ingress permitted when resolves": {
			Annotations: map[string]string{
				"kubernetes.io/ingress.class":    "nginx-external",
				"stable.k8s.psg.io/kcm.provider": "http",
			},
			Labels:   map[string]string{"stable.k8s.psg.io/kcm.class": "default"},
			Hosts:    []string{"site.nohere.com"},
			Resolves: config.ExternalIngressHostname,
		},
		"check a ingress is permitted when the dns check is disabled": {
			Annotations: map[string]string{
				"kubernetes.io/ingress.class":    "nginx-external",
				"stable.k8s.psg.io/kcm.provider": "http",
			},
			Namespace: map[string]string{
				"policy-admission.acp.homeoffice.gov.uk/kubecertmanager/enable-dns-check": "false",
			},
			Labels:   map[string]string{"stable.k8s.psg.io/kcm.class": "default"},
			Hosts:    []string{"site.nohere.com"},
			Resolves: "bad.hostname",
		},
		"check a ingress is denied when dns check is enabled": {
			Annotations: map[string]string{
				"kubernetes.io/ingress.class":    "nginx-external",
				"stable.k8s.psg.io/kcm.provider": "http",
			},
			Namespace: map[string]string{
				"policy-admission.acp.homeoffice.gov.uk/kubecertmanager/enable-dns-check": "true",
			},
			Labels:   map[string]string{"stable.k8s.psg.io/kcm.class": "default"},
			Hosts:    []string{"site.nohere.com"},
			Resolves: "bad.hostname",
			Errors: field.ErrorList{
				{
					Field:    "spec.rules[0].host",
					BadValue: "site.nohere.com",
					Type:     field.ErrorTypeInvalid,
					Detail:   "the hostname: site.nohere.com is not pointed to the external ingress dns name ingress.acp.example.com",
				},
			},
		},
		"check a deault value of dns check is true": {
			Annotations: map[string]string{
				"kubernetes.io/ingress.class":    "nginx-external",
				"stable.k8s.psg.io/kcm.provider": "http",
			},
			Namespace: map[string]string{
				"policy-admission.acp.homeoffice.gov.uk/kubecertmanager/enable-dns-check": "bad_value",
			},
			Labels:   map[string]string{"stable.k8s.psg.io/kcm.class": "default"},
			Hosts:    []string{"site.nohere.com"},
			Resolves: "bad.hostname",
			Errors: field.ErrorList{
				{
					Field:    "spec.rules[0].host",
					BadValue: "site.nohere.com",
					Type:     field.ErrorTypeInvalid,
					Detail:   "the hostname: site.nohere.com is not pointed to the external ingress dns name ingress.acp.example.com",
				},
			},
		},
	}
	newTestAuthorizer(t, config).runChecks(t, checks)
}

type kubeCertCheck struct {
	Annotations map[string]string
	Namespace   map[string]string
	Errors      field.ErrorList
	Hosts       []string
	Labels      map[string]string
	Resolves    string
}

type testAuthorizer struct {
	config *Config
	svc    api.Authorize
}

type testResolver struct {
	hostname string
}

func (t *testResolver) GetCNAME(string) (string, error) {
	return t.hostname, nil
}

func newTestAuthorizer(t *testing.T, config *Config) *testAuthorizer {
	if config == nil {
		config = newTestConfig()
	}
	c, err := New(config)
	c.(*authorizer).resolve = &testResolver{}
	if err != nil {
		t.Fatalf("unable to create authorizer: %s", err)
	}

	return &testAuthorizer{config: config, svc: c}
}

func (c *testAuthorizer) runChecks(t *testing.T, checks map[string]kubeCertCheck) {
	for desc, check := range checks {
		cx := newTestContext()

		cx.Client.CoreV1().Namespaces().Create(&v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "test",
				Annotations: check.Namespace,
			},
		})

		if check.Resolves != "" {
			c.svc.(*authorizer).resolve = &testResolver{hostname: check.Resolves}
		}

		ingress := newDefaultIngress()
		for _, x := range check.Hosts {
			ingress.Spec.Rules = append(ingress.Spec.Rules, extensions.IngressRule{Host: x})
		}
		ingress.ObjectMeta.Annotations = check.Annotations
		ingress.ObjectMeta.Labels = check.Labels
		cx.Object = ingress

		assert.Equal(t, check.Errors, c.svc.Admit(context.TODO(), cx), "case: '%s' result not as expected", desc)
	}
}

func newTestContext() *api.Context {
	return &api.Context{
		Cache:  cache.New(1*time.Minute, 1*time.Minute),
		Client: fake.NewSimpleClientset(),
		Prefix: "policy-admission.acp.homeoffice.gov.uk",
	}
}

func newDefaultIngress() *extensions.Ingress {
	return &extensions.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-",
			Namespace: "test",
		},
		Spec: extensions.IngressSpec{},
	}
}

func newTestConfig() *Config {
	return &Config{
		IgnoreNamespaces: []string{"kube-system"},
	}
}
