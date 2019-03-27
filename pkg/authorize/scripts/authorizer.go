/*
Copyright 2018 Home Office All rights reserved.

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

package scripts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/UKHomeOffice/policy-admission/pkg/api"
	"github.com/UKHomeOffice/policy-admission/pkg/utils"

	"github.com/robertkrimen/otto"
	log "github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

var errTimeout = errors.New("operation timed out")

// authorizer is used to wrap the interaction with the psp runtime
type authorizer struct {
	// the configuration for the enforcer
	config *Config
}

// Admit is responsible for authorizing the pod
func (c *authorizer) Admit(ctx context.Context, cx *api.Context) field.ErrorList {
	var errs field.ErrorList

	// @step: decode the object into an object
	// @TODO there probably a better way of doing the, perhaps just passing the object??
	err := func() error {
		obj, err := marshal(cx.Object)
		if err != nil {
			return err
		}
		// @note: a somewhat hacky but's its due to the fact the groupkind is omitted
		if _, found := obj["apiVersion"]; !found {
			obj["apiVersion"] = fmt.Sprintf("%s/%s", cx.Group.Group, cx.Group.Version)
		}
		if _, found := obj["kind"]; !found {
			obj["kind"] = cx.Group.Kind
		}

		// @step: create the runtime
		vm := otto.New()
		for k, v := range c.config.Options {
			vm.Set(k, v)
		}
		vm.Set("cache", cx.Cache)
		vm.Set("object", obj)

		if cx.Object.GetNamespace() != "" {
			// @step: get namespace for this object
			namespace, err := utils.GetCachedNamespace(cx.Client, cx.Cache, cx.Object.GetNamespace())
			if err != nil {
				return err
			}
			ns, err := marshal(namespace)
			if err != nil {
				return err
			}
			vm.Set("namespace", ns)
		}

		// @step: setup some functions
		vm.Set("log", func(call otto.FunctionCall) otto.Value {
			log.WithFields(log.Fields{
				"scripts": c.config.Name,
			}).Info(call.Argument(0).String())

			return otto.Value{}
		})

		vm.Set("hasPrefix", hasPrefix)
		vm.Set("hasSuffix", hasSuffix)
		vm.Set("inDomain", inDomain)

		vm.Set("deny", func(call otto.FunctionCall) otto.Value {
			path := call.Argument(0).String()
			message := call.Argument(1).String()
			value := call.Argument(2).String()
			errs = append(errs, field.Invalid(field.NewPath(path), value, message))

			return otto.Value{}
		})

		return c.runSafely(ctx, vm, c.config.Script, c.config.Timeout)
	}()
	if err != nil {
		return append(errs, field.InternalError(field.NewPath(""), err))
	}

	return errs
}

// runSafely runs the script in a safe manner returning the result
func (c *authorizer) runSafely(ctx context.Context, e *otto.Otto, script string, timeout time.Duration) (err error) {
	defer func() {
		if e := recover(); e != nil {
			if e == errTimeout {
				err = errTimeout
				return
			}
			panic(e)
		}
	}()
	e.Interrupt = make(chan func(), 1)

	// @check for a timeout and if not there, set the default
	if timeout <= 0 {
		timeout = NewDefaultConfig().Timeout
	}

	// @step: setup a timer and done channel
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	go func() {
		select {
		case <-ctx.Done():
			if err := ctx.Err(); err != nil {
				e.Interrupt <- func() {
					panic(errTimeout)
				}
			}
		}
	}()

	_, err = e.Run(script)

	return err
}

// FilterOn returns the authorizer handle
func (c *authorizer) FilterOn() *api.Filter {
	return &c.config.FilterOn
}

// Name returns the name of the provider
func (c *authorizer) Name() string {
	return c.config.Name
}

// New creates and returns an authorizer
func New(config *Config) (api.Authorize, error) {
	if config == nil {
		config = NewDefaultConfig()
	}

	return &authorizer{config: config}, nil
}

// NewFromFile reads the configuration path and returns the authorizer
func NewFromFile(path string) (api.Authorize, error) {
	if path == "" {
		return New(nil)
	}
	cfg := &Config{}
	if err := utils.NewConfig(path).Read(cfg); err != nil {
		return nil, err
	}
	if cfg.FilterOn.Kind == "" {
		cfg.FilterOn.Kind = api.FilterAll
	}
	if len(cfg.IgnoreNamespaces) != 0 {
		cfg.FilterOn.IgnoreNamespaces = cfg.IgnoreNamespaces
	} else {
		cfg.FilterOn.IgnoreNamespaces = []string{"kube-system", "kube-public", "kube-admission"}
	}

	return New(cfg)
}

// marshal converts the object for us
func marshal(o interface{}) (map[string]interface{}, error) {
	data := make(map[string]interface{}, 0)

	encoded, err := json.Marshal(o)
	if err != nil {
		return data, err
	}

	if err := json.Unmarshal(encoded, &data); err != nil {
		return data, err
	}

	return data, nil
}

// hasImage checks if a group of containers is using a particular image
func hasImage(filter string, containers []map[string]interface{}) (bool, error) {
	matcher, err := regexp.Compile(filter)
	if err != nil {
		return false, err
	}

	for _, x := range containers {
		if im, found := x["image"]; found {
			if matcher.MatchString(fmt.Sprintf("%v", im)) {
				return true, nil
			}
		}
	}

	return false, nil
}

// Stop is called when the authorizer is being shutdown
func (c *authorizer) Stop() error {
	return nil
}
