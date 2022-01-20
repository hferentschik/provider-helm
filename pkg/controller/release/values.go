/*
Copyright 2020 The Crossplane Authors.

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

package release

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/crossplane-contrib/provider-helm/apis/release/v1beta1"

	"github.com/pkg/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

const (
	keyDefaultValuesFrom = "values.yaml"
	keyDefaultSet        = "value"
)

const (
	errFailedToUnmarshalDesiredValues = "failed to unmarshal desired values"
	errFailedParsingSetData           = "failed parsing --set data"
	errFailedToGetValueFromSource     = "failed to get value from source"
	errMissingValueForSet             = "missing value for --set"
)

var (
	pathElemRegexp = regexp.MustCompile(`^(.+)\[(\d+)\]$`)
)

type pathElement struct {
	name  string
	index *int
}

func (p *pathElement) setValue(data map[string]interface{}, value string) {
	if p.index == nil {
		data[p.name] = value
	} else {
		var list []interface{}
		_, exists := data[p.name]
		if exists {
			list = data[p.name].([]interface{})
		} else {
			list = []interface{}{}
		}
		data[p.name] = p.indexValue(list, *p.index, value)
	}
}

func (p *pathElement) traverse(data map[string]interface{}) map[string]interface{} {
	_, exists := data[p.name]
	var v map[string]interface{}
	if exists {
		v = p.entry(data)
	} else {
		v = p.newEntry(data)
	}

	return v
}

func (p *pathElement) newEntry(data map[string]interface{}) map[string]interface{} {
	tmp := map[string]interface{}{}
	if p.index == nil {
		data[p.name] = tmp
	} else {
		list := p.indexValue([]interface{}{}, *p.index, tmp)

		data[p.name] = list
	}
	return tmp
}

func (p *pathElement) indexValue(list []interface{}, index int, val interface{}) []interface{} {
	if len(list) <= index {
		newList := make([]interface{}, index+1)
		copy(newList, list)
		list = newList
	}
	list[index] = val
	return list
}

func (p *pathElement) entry(data map[string]interface{}) map[string]interface{} {
	if p.index == nil {
		return data[p.name].(map[string]interface{})
	}
	list := data[p.name].([]interface{})
	return list[*p.index].(map[string]interface{})
}

func newPathElement(s string) (pathElement, error) {
	matches := pathElemRegexp.FindStringSubmatch(s)
	var elem pathElement
	if matches == nil {
		elem = pathElement{name: s}
	} else {
		index, _ := strconv.Atoi(matches[2])
		if index < 0 {
			return pathElement{}, fmt.Errorf("negative %d index not allowed", index)
		}
		elem = pathElement{name: matches[1], index: &index}
	}
	return elem, nil
}

func composeValuesFromSpec(ctx context.Context, kube client.Client, spec v1beta1.ValuesSpec) (map[string]interface{}, error) {
	base := map[string]interface{}{}

	for _, vf := range spec.ValuesFrom {
		s, err := getDataValueFromSource(ctx, kube, vf, keyDefaultValuesFrom)
		if err != nil {
			return nil, errors.Wrap(err, errFailedToGetValueFromSource)
		}

		var currVals map[string]interface{}
		if err = yaml.Unmarshal([]byte(s), &currVals); err != nil {
			return nil, errors.Wrap(err, errFailedToUnmarshalDesiredValues)
		}
		base = mergeMaps(base, currVals)
	}

	var inlineVals map[string]interface{}
	err := yaml.Unmarshal(spec.Values.Raw, &inlineVals)
	if err != nil {
		return nil, errors.Wrap(err, errFailedToUnmarshalDesiredValues)
	}

	base = mergeMaps(base, inlineVals)

	for _, s := range spec.Set {
		v := ""
		if s.Value != "" {
			v = s.Value
		}
		if s.ValueFrom != nil {
			v, err = getDataValueFromSource(ctx, kube, *s.ValueFrom, keyDefaultSet)
			if err != nil {
				return nil, errors.Wrap(err, errFailedToGetValueFromSource)
			}
		}

		if v == "" {
			return nil, errors.New(errMissingValueForSet)
		}

		if err := setValue(s.Name, base, v); err != nil {
			return nil, errors.Wrap(err, errFailedParsingSetData)
		}
	}

	return base, nil
}

func setValue(name string, data map[string]interface{}, value string) error {
	pathElements := strings.Split(name, ".")
	v := data
	for i, pathElement := range pathElements {
		elem, err := newPathElement(pathElement)
		if err != nil {
			return errors.Wrap(err, "unable to create path element")
		}
		if i == len(pathElements)-1 {
			elem.setValue(v, value)
		} else {
			v = elem.traverse(v)
		}
	}
	return nil
}

// Copied from helm cli
// https://github.com/helm/helm/blob/9bc7934f350233fa72a11d2d29065aa78ab62792/pkg/cli/values/options.go#L88
func mergeMaps(a, b map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(a))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		if v, ok := v.(map[string]interface{}); ok {
			if bv, ok := out[k]; ok {
				if bv, ok := bv.(map[string]interface{}); ok {
					out[k] = mergeMaps(bv, v)
					continue
				}
			}
		}
		out[k] = v
	}
	return out
}
